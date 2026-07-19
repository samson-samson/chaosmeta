/*
 * DB orchestration — replaces Argo Workflow as the experiment execution driver (pure logic).
 *
 * See docs/design/fi-db-orchestration.md. Nodes are driven by a DB state machine on
 * `workflow_node_instance` rows (Row/Column topology, Status, Version, ExecType) + a reconcile loop,
 * instead of an Argo Workflow CR whose DAG created the chaosmeta CRs. The only external execution
 * dependency left is the chaosmeta operator.
 *
 * This file is deliberately PURE: it depends on no K8s client and no beego/ORM model package — only
 * on the injectable NodeExecutor + nodeStore interfaces and the node STRUCT type. That keeps
 * ReconcileInstance unit-testable with fakes (design §6: "mock chaosmetaService 接口"). All production
 * wiring (real ChaosmetaService adapter, real model-backed nodeStore, CR-name resolution via the basic
 * model) lives in orchestrator_prod.go.
 *
 * Scope: incremental — legacy argo_workflow.go / experiment_custom_resource.go are kept (not deleted)
 * to limit blast radius; routine.go simply no longer calls NewArgoWorkFlowService. K8s+operator real
 * verification is left to the cluster environment (honest boundary).
 */

package experiment

import (
	"context"

	experimentInstanceModel "chaosmeta-platform/pkg/models/experiment_instance"
)

// nodeStatus mirrors the workflow_node_instance.Status values (model default is "to_be_executed").
// Centralized here as the orchestrator's state-machine vocabulary rather than re-deriving strings.
const (
	nodeStatusToBeExecuted = "to_be_executed"
	nodeStatusRunning      = "running"
	nodeStatusSucceeded    = "succeeded"
	nodeStatusFailed       = "failed"
	nodeStatusStopped      = "stopped"
	nodeStatusError        = "error"
)

// NodeExecutor is the Get/Create/Recover surface for ONE chaosmeta CR (inject/flow/measure). It
// abstracts the concrete *ChaosmetaService so reconcile can be unit-tested with a fake. Name() resolves
// the CR name — production does the basic-model lookups (see crNameForNode); tests script it.
//
// GetStatus contract: return the CR's status string, or "" when the CR is absent OR has no status yet.
// The reconciler disambiguates by context (running poll vs recover confirm)— see call sites.
type NodeExecutor interface {
	// Name returns the chaosmeta CR name this node mints (same algorithm as the legacy Argo path, so
	// stop/confirm reverse-lookup the same CR).
	Name() (string, error)
	// GetStatus returns the CR's (phase, status). phase is the chaosmeta CR's Spec/Status.Phase
	// ("inject"/"recover" for fault experiments; "" for flow/measure which carry no Phase field).
	// status is "" if the CR is absent / not yet statused. The reconciler uses phase to tell a fault
	// that has merely FINISHED INJECTING (phase=inject,status=success) from one that has RECOVERED
	// (phase=recover,status=success) — the former must NOT count as clean, or the next node starts
	// while this fault is still resident (叠加注入). This mirrors the legacy Argo fault SuccessCondition
	// "status.phase==recover,status.status==success" (experiment_custom_resource.go:105).
	GetStatus(ctx context.Context, namespace, name string) (phase, status string, err error)
	// Create creates the CR for the node. Implementations marshal their concrete CR struct.
	Create(ctx context.Context) (string, error)
	// Recover flips the named CR to recover phase. Must be idempotent w.r.t. an absent CR.
	Recover(namespace, name string) error
}

// nodeStore is the DB surface reconcile needs, abstracted so tests inject an in-memory fake instead of
// standing up beego ORM. The production adapter wraps the experiment_instance model funcs.
type nodeStore interface {
	// ListByExperiment returns the instance's nodes ordered by row, column (the model query orders).
	ListByExperiment(instanceUUID string) ([]*experimentInstanceModel.WorkflowNodeInstance, error)
	// UpdateStatus writes a node's status (+ optional message). The model's optimistic Version lock
	// guards against two reconcile ticks racing on the same node.
	UpdateStatus(uuid, status, message string) error
}

// Reconciler drives one experiment instance's nodes forward from the DB. It owns no K8s state: the
// executor factory + nodeStore + namespace are injected. The factory is called per-node per-tick, so it
// must be cheap (the production factory builds a ChaosmetaService from a cached rest.Config).
type Reconciler struct {
	// newExecutor builds a NodeExecutor for a node at a given phase. Bound once at construction.
	newExecutor func(node *experimentInstanceModel.WorkflowNodeInstance, phase PhaseType) (NodeExecutor, error)
	store       nodeStore
	namespace   func() string
}

// ReconcileInstance advances one experiment instance's nodes by one tick. Nodes are processed in
// Row/Column order; the topology is strictly serial — a node starts only once its predecessor has
// succeeded. Returns the derived instance-level status (Workflow* constants from routine.go).
//
// DB equivalent of the Argo DAG controller: instead of Argo creating CRs per DAG task and reporting
// NodeStatus, the reconciler creates CRs directly via NodeExecutor and polls their status. Idempotent
// and safe to call from both the synchronous start path and a background ticker.
func (r *Reconciler) ReconcileInstance(instanceUUID string) (string, error) {
	nodes, err := r.store.ListByExperiment(instanceUUID)
	if err != nil {
		return WorkflowFailed, err
	}
	if len(nodes) == 0 {
		return WorkflowSucceeded, nil
	}
	ctx := context.Background()
	ns := r.namespace()
	prevSucceeded := true // first node has no predecessor
	failed := false
	running := false
	for _, node := range nodes {
		switch node.Status {
		case nodeStatusSucceeded:
			prevSucceeded = true
			continue
		case nodeStatusFailed, nodeStatusError:
			failed = true
			prevSucceeded = false
			continue
		case nodeStatusStopped:
			prevSucceeded = false // stopped nodes don't advance the chain
			continue
		case nodeStatusToBeExecuted:
			if !prevSucceeded {
				running = true // waiting on a prior node; nothing to start this tick
				continue
			}
			if err := r.startNode(ctx, ns, node); err != nil {
				_ = r.store.UpdateStatus(node.UUID, nodeStatusFailed, err.Error())
				failed = true
				prevSucceeded = false
				continue
			}
			// startNode may complete a node immediately (Wait nodes have no CR). If so, the chain
			// advances same-tick (prevSucceeded=true) and this node does NOT keep the instance running.
			// Otherwise it's now running → blocks successors and marks the instance running.
			if node.Status == nodeStatusSucceeded {
				prevSucceeded = true
			} else {
				running = true
				prevSucceeded = false
			}
		case nodeStatusRunning:
			if r.pollRunning(ctx, ns, node) {
				prevSucceeded = true // node completed cleanly this tick
			} else {
				// Still running (pollRunning left it that way) → instance is running and the chain
				// is blocked here for the rest of this tick.
				running = true
				if node.Status == nodeStatusFailed || node.Status == nodeStatusError {
					failed = true
				}
				prevSucceeded = false
			}
		}
	}
	switch {
	case failed:
		return WorkflowFailed, nil
	case running:
		return WorkflowRunning, nil
	default:
		return WorkflowSucceeded, nil
	}
}

// pollRunning checks a running node's CR and completes or fails it. Returns true iff the node became
// succeeded this tick. Mutates node.Status in place so the caller's prevSucceeded tracking sees it.
//
// Empty status ("") is treated as "not yet statused" → leave running (do NOT falsely complete).
//
// CRITICAL (Codex P1-1): a FAULT CR reports status=success as soon as the inject phase finishes
// (chaosmeta-inject-operator inject handler), BEFORE recover flips it. The fault is still resident at
// that point. We may only mark the node succeeded when the CR is genuinely clean — for faults that
// means phase==recover && status∈{success,stopped} (mirrors the legacy Argo success condition). Marking
// it succeeded earlier would let the strict-serial reconciler start the NEXT node while this fault is
// still injected → 叠加注入. flow/measure CRs carry no Phase field (their SuccessCondition is just
// status==success, inject is terminal) so they're clean on status alone.
func (r *Reconciler) pollRunning(ctx context.Context, ns string, node *experimentInstanceModel.WorkflowNodeInstance) bool {
	ex, err := r.newExecutor(node, RecoverPhaseType)
	if err != nil {
		_ = r.store.UpdateStatus(node.UUID, nodeStatusError, err.Error())
		node.Status = nodeStatusError
		return false
	}
	name, err := ex.Name()
	if err != nil {
		_ = r.store.UpdateStatus(node.UUID, nodeStatusError, err.Error())
		node.Status = nodeStatusError
		return false
	}
	phase, st, _ := ex.GetStatus(ctx, ns, name)
	if isCRStatusFailed(st) {
		_ = r.store.UpdateStatus(node.UUID, nodeStatusFailed, st)
		node.Status = nodeStatusFailed
		return false
	}
	// Issue recover FIRST (so the operator flips inject→recover) and only then treat success as clean.
	// For fault nodes still in inject phase with status=success, issue Recover and leave the node
	// running — the next tick will observe phase=recover,status=success and then complete it. This is
	// the fix for P1-1: never complete a fault on an inject-phase success.
	if ExecType(node.ExecType) == FaultExecType && st == string(SuccessStatusType) && phase != string(RecoverPhaseType) {
		_ = ex.Recover(ns, name) // best-effort; ticker re-observes next tick even if this errors
		return false             // still running — do not advance the chain
	}
	if isCRStatusClean(st, phase) {
		if err := r.completeNode(ctx, ns, node, ex, name); err != nil {
			_ = r.store.UpdateStatus(node.UUID, nodeStatusFailed, err.Error())
			node.Status = nodeStatusFailed
			return false
		}
		node.Status = nodeStatusSucceeded
		return true
	}
	// "" (not yet statused) or a non-terminal status → leave running.
	return false
}

// startNode creates the chaosmeta CR for a node and marks it running. WaitExecType nodes have no CR —
// the minimal DB loop advances them to succeeded directly on the start tick (the legacy "before-wait"
// duration was an Argo suspend; wall-clock wait gating in the DB loop is a cluster-side refinement left
// to the operator/user — see design §6 honest boundary).
func (r *Reconciler) startNode(ctx context.Context, ns string, node *experimentInstanceModel.WorkflowNodeInstance) error {
	if ExecType(node.ExecType) == WaitExecType {
		return r.store.UpdateStatus(node.UUID, nodeStatusSucceeded, "")
	}
	ex, err := r.newExecutor(node, DriverPhaseFor(node))
	if err != nil {
		return err
	}
	if _, err := ex.Create(ctx); err != nil {
		return err
	}
	return r.store.UpdateStatus(node.UUID, nodeStatusRunning, "")
}

// completeNode marks a node succeeded and, for fault nodes, issues the recover order so the injected
// fault is withdrawn (mirrors Argo's success condition requiring phase==recover,status==success). The
// recover is best-effort here; ConfirmRecoverByDB double-checks cleanliness afterward.
func (r *Reconciler) completeNode(ctx context.Context, ns string, node *experimentInstanceModel.WorkflowNodeInstance, ex NodeExecutor, name string) error {
	if ExecType(node.ExecType) == FaultExecType {
		_ = ex.Recover(ns, name)
	}
	return r.store.UpdateStatus(node.UUID, nodeStatusSucceeded, "")
}

// StopInstanceByDB issues recover on every resident node (running or previously succeeded) and marks
// them stopped. DB replacement for the Argo shutdown path; depends on no Argo Workflow CR existing.
func (r *Reconciler) StopInstanceByDB(instanceUUID string) error {
	nodes, err := r.store.ListByExperiment(instanceUUID)
	if err != nil {
		return err
	}
	ns := r.namespace()
	for _, node := range nodes {
		if node.Status != nodeStatusRunning && node.Status != nodeStatusSucceeded {
			continue
		}
		if ExecType(node.ExecType) == WaitExecType {
			_ = r.store.UpdateStatus(node.UUID, nodeStatusStopped, "")
			continue
		}
		ex, exerr := r.newExecutor(node, RecoverPhaseType)
		if exerr != nil {
			continue
		}
		name, _ := ex.Name()
		_ = ex.Recover(ns, name) // absent CR is fine → treated as already cleaned
		_ = r.store.UpdateStatus(node.UUID, nodeStatusStopped, "")
	}
	return nil
}

// ConfirmRecoverByDB does a SINGLE pass over the instance's fault nodes and returns the CR names that
// are not yet confirmed clean (absent CR counts as clean — it was GC'd after recover). Empty result ==
// all clean. The caller (background ticker) re-invokes until it returns empty; we deliberately do NOT
// sleep/poll internally so this stays cheap and unit-testable.
//
// DB replacement for confirmRecoverCompleted that no longer reads Argo Workflow.Status.Nodes.
func (r *Reconciler) ConfirmRecoverByDB(instanceUUID string) []string {
	nodes, err := r.store.ListByExperiment(instanceUUID)
	if err != nil {
		return []string{"<db-unavailable>"}
	}
	ctx := context.Background()
	ns := r.namespace()
	var unclean []string
	for _, node := range nodes {
		if ExecType(node.ExecType) != FaultExecType {
			continue
		}
		ex, exerr := r.newExecutor(node, RecoverPhaseType)
		if exerr != nil {
			unclean = append(unclean, node.UUID)
			continue
		}
		name, _ := ex.Name()
		phase, st, _ := ex.GetStatus(ctx, ns, name)
		if st == "" {
			continue // absent CR → already GC'd after recover, treat as clean
		}
		if isCRStatusClean(st, phase) {
			continue // clean terminal → recovered
		}
		unclean = append(unclean, name)
	}
	return unclean
}

// DriverPhaseFor returns the phase a node's CR is created in — always inject first; recover is issued
// later via Executor.Recover. Exposed for the production executor factory to reuse.
func DriverPhaseFor(node *experimentInstanceModel.WorkflowNodeInstance) PhaseType {
	return InjectPhaseType
}

// isCRStatusClean reports whether a chaosmeta CR is at a clean terminal (no longer injecting).
//
// Codex P1-1 fix: for FAULT experiments the CR has a Phase field. The inject operator sets
// status=success as soon as the INJECT phase finishes — but the fault is still resident until the
// RECOVER phase reaches a terminal. So a fault is only clean when phase==recover AND status is a clean
// terminal. flow/measure CRs have no Phase (phase==""); their success is inject-terminal, so they're
// clean on status alone (mirrors the legacy Argo SuccessCondition difference at
// experiment_custom_resource.go:105 vs :121).
func isCRStatusClean(s, phase string) bool {
	if s == "" {
		return false
	}
	if !isCleanTerminal(StatusType(s)) {
		return false
	}
	// No phase carried (flow/measure) → status-terminal is sufficient.
	if phase == "" {
		return true
	}
	// Fault with a phase → require recover phase so an inject-phase success is NOT treated as clean.
	return PhaseType(phase) == RecoverPhaseType
}

// isCRStatusFailed reports a failed CR status; used to short-circuit a running node into failed.
func isCRStatusFailed(s string) bool {
	return StatusType(s) == FailedStatusType
}
