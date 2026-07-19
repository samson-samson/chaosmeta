/*
 * orchestrator_test.go — unit tests for the DB-orchestration reconcile loop (pure logic).
 *
 * Design §6 收口点: "单测：orchestrator reconcile 纯逻辑（mock chaosmetaService 接口，验证串行推进/
 * 失败传播/stop 恢复）". These tests inject an in-memory nodeStore + scripted NodeExecutors — no K8s,
 * no beego ORM. They cover the three behaviors the Argo path used to guarantee:
 *   1. serial progression — a node only runs after its predecessor succeeded (strict-serial topology);
 *   2. failure propagation — a failed node fails the instance and blocks later nodes;
 *   3. stop recovery — StopInstanceByDB marks resident nodes stopped without leaving them hanging;
 *   4. confirm — ConfirmRecoverByDB reports unclean fault CRs and treats absent CRs as clean.
 */

package experiment

import (
	"context"
	"fmt"
	"testing"

	experimentInstanceModel "chaosmeta-platform/pkg/models/experiment_instance"
)

// ---- in-memory nodeStore ----

type fakeStore struct {
	nodes []*experimentInstanceModel.WorkflowNodeInstance
	reads map[string]int // per-uuid UpdateStatus call count (debug aid)
}

func newFakeStore(rows, cols int, execType string) *fakeStore {
	s := &fakeStore{reads: map[string]int{}}
	var order int
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			order++
			s.nodes = append(s.nodes, &experimentInstanceModel.WorkflowNodeInstance{
				UUID:                   fmt.Sprintf("n-%d", order),
				ExperimentInstanceUUID: "inst-1",
				Row:                    r,
				Column:                 c,
				ExecType:               execType,
				Status:                 nodeStatusToBeExecuted,
			})
		}
	}
	return s
}

func (s *fakeStore) ListByExperiment(string) ([]*experimentInstanceModel.WorkflowNodeInstance, error) {
	return s.nodes, nil
}

func (s *fakeStore) UpdateStatus(uuid, status, message string) error {
	for _, n := range s.nodes {
		if n.UUID == uuid {
			n.Status = status
			s.reads[uuid]++
			return nil
		}
	}
	return fmt.Errorf("node %s not found", uuid)
}

// ---- scripted NodeExecutor ----

type fakeExecutor struct {
	name      string
	phase     string // what GetStatus returns as phase (fault: "inject"/"recover"; flow/measure: "")
	status    string // what GetStatus returns once "running"
	creates   int
	recovers  int
	failCreate error
}

func (e *fakeExecutor) Name() (string, error)                                          { return e.name, nil }
func (e *fakeExecutor) GetStatus(context.Context, string, string) (string, string, error) { return e.phase, e.status, nil }
func (e *fakeExecutor) Create(context.Context) (string, error)                        { e.creates++; return e.name, e.failCreate }
func (e *fakeExecutor) Recover(string, string) error                                   { e.recovers++; return nil }

// fakeFactory always builds an injector regardless of phase; the test sets status per node on demand
// by mutating execBody. We key on node.UUID.
func fakeFactory(execs map[string]*fakeExecutor) func(*experimentInstanceModel.WorkflowNodeInstance, PhaseType) (NodeExecutor, error) {
	return func(node *experimentInstanceModel.WorkflowNodeInstance, _ PhaseType) (NodeExecutor, error) {
		ex, ok := execs[node.UUID]
		if !ok {
			return nil, fmt.Errorf("no fake executor for %s", node.UUID)
		}
		return ex, nil
	}
}

func reconciler(s *fakeStore, execs map[string]*fakeExecutor) *Reconciler {
	return &Reconciler{
		store:     s,
		namespace: func() string { return "ns" },
		newExecutor: fakeFactory(execs),
	}
}

// ---- tests ----

// TestReconcileSerialProgression: two fault nodes in series. Tick 1 starts node-1 (running); node-2 must
// NOT start yet (predecessor not succeeded). Once node-1's CR reports clean, a later tick moves node-1
// to succeeded AND starts node-2. Validates the strict-serial invariant.
func TestReconcileSerialProgression(t *testing.T) {
	store := newFakeStore(1, 2, string(FaultExecType)) // row0: n-1, n-2 serial in-column
	n1, n2 := store.nodes[0], store.nodes[1]
	e1 := &fakeExecutor{name: "cr-n1", status: ""} // "" → not yet statused
	e2 := &fakeExecutor{name: "cr-n2", status: ""}
	r := reconciler(store, map[string]*fakeExecutor{n1.UUID: e1, n2.UUID: e2})

	// Tick 1: n-1 should be created + running; n-2 still to_be_executed (serial gate).
	if st, _ := r.ReconcileInstance("inst-1"); st != WorkflowRunning {
		t.Fatalf("tick1: want Running, got %s (n1.creates=%d)", st, e1.creates)
	}
	if e1.creates != 1 {
		t.Fatalf("tick1: n1 not started (creates=%d)", e1.creates)
	}
	if n1.Status != nodeStatusRunning || n2.Status != nodeStatusToBeExecuted {
		t.Fatalf("tick1: n1=%s n2=%s, want n1=running n2=to_be_executed", n1.Status, n2.Status)
	}
	if e2.creates != 0 {
		t.Fatalf("tick1: n2 started before n1 succeeded (serial violation), creates=%d", e2.creates)
	}

	// Tick 2: n-1 CR now reports clean (RECOVER phase + success) → complete n-1 + start n-2.
	e1.phase = string(RecoverPhaseType)
	e1.status = string(SuccessStatusType)
	if st, _ := r.ReconcileInstance("inst-1"); st != WorkflowRunning {
		t.Fatalf("tick2: want Running, got %s", st)
	}
	if n1.Status != nodeStatusSucceeded {
		t.Fatalf("tick2: n1=%s want succeeded", n1.Status)
	}
	if n2.Status != nodeStatusRunning || e2.creates != 1 {
		t.Fatalf("tick2: n2=%s creates=%d, want running+started", n2.Status, e2.creates)
	}

	// Tick 3: n-2 clean → instance succeeds.
	e2.phase = string(RecoverPhaseType)
	e2.status = string(SuccessStatusType)
	if st, _ := r.ReconcileInstance("inst-1"); st != WorkflowSucceeded {
		t.Fatalf("tick3: want Succeeded, got %s", st)
	}
	if n2.Status != nodeStatusSucceeded {
		t.Fatalf("tick3: n2=%s want succeeded", n2.Status)
	}
}

// TestReconcileFailurePropagation: a failed node must fail the instance and gate later nodes.
func TestReconcileFailurePropagation(t *testing.T) {
	store := newFakeStore(1, 2, string(FaultExecType))
	n1, n2 := store.nodes[0], store.nodes[1]
	e1 := &fakeExecutor{name: "cr-n1", status: string(RunningStatusType)}
	e2 := &fakeExecutor{name: "cr-n2", status: ""}
	r := reconciler(store, map[string]*fakeExecutor{n1.UUID: e1, n2.UUID: e2})

	// Tick 1: start n-1.
	r.ReconcileInstance("inst-1")
	if n1.Status != nodeStatusRunning {
		t.Fatalf("tick1: n1=%s want running", n1.Status)
	}

	// Tick 2: n-1 CR reports failed → n-1 failed, instance Failed, n-2 never started.
	e1.status = string(FailedStatusType)
	st, _ := r.ReconcileInstance("inst-1")
	if st != WorkflowFailed {
		t.Fatalf("tick2: want Failed, got %s", st)
	}
	if n1.Status != nodeStatusFailed {
		t.Fatalf("tick2: n1=%s want failed", n1.Status)
	}
	if e2.creates != 0 {
		t.Fatalf("tick2: n2 started despite predecessor failure, creates=%d", e2.creates)
	}
}

// TestReconcileWaitNodeAdvances: a Wait node has no CR — the minimal DB loop marks it succeeded on the
// start tick, so a successor node can start the same tick instance is reconciled.
func TestReconcileWaitNodeAdvances(t *testing.T) {
	store := newFakeStore(1, 2, string(WaitExecType)) // both wait nodes
	n1 := store.nodes[0]
	r := reconciler(store, map[string]*fakeExecutor{}) // wait needs no executor

	st, _ := r.ReconcileInstance("inst-1")
	if st != WorkflowSucceeded {
		t.Fatalf("want Succeeded (both wait nodes complete same tick), got %s", st)
	}
	if n1.Status != nodeStatusSucceeded {
		t.Fatalf("wait node should be succeeded immediately, got %s", n1.Status)
	}
	if store.nodes[1].Status != nodeStatusSucceeded {
		t.Fatalf("second wait node should be succeeded, got %s", store.nodes[1].Status)
	}
}

// TestStopInstanceByDB: stop marks every running/succeeded node stopped and issues recover on faults.
func TestStopInstanceByDB(t *testing.T) {
	store := newFakeStore(1, 2, string(FaultExecType))
	n1, n2 := store.nodes[0], store.nodes[1]
	n1.Status = nodeStatusRunning
	n2.Status = nodeStatusSucceeded
	e1 := &fakeExecutor{name: "cr-n1"}
	e2 := &fakeExecutor{name: "cr-n2"}
	r := reconciler(store, map[string]*fakeExecutor{n1.UUID: e1, n2.UUID: e2})

	if err := r.StopInstanceByDB("inst-1"); err != nil {
		t.Fatalf("StopInstanceByDB error: %v", err)
	}
	if n1.Status != nodeStatusStopped || n2.Status != nodeStatusStopped {
		t.Fatalf("want both stopped, got n1=%s n2=%s", n1.Status, n2.Status)
	}
	if e1.recovers != 1 || e2.recovers != 1 {
		t.Fatalf("want 1 recover each, got e1=%d e2=%d", e1.recovers, e2.recovers) // succeeded faults also re-recover on stop (idempotent)
	}
}

// TestConfirmRecoverByDB: clean/absent fault CRs → empty; a not-clean CR → reported by name. Also pins
// the Codex P1-1 fix: a fault CR in the INJECT phase (even with status=success) is NOT clean — the fault
// is still resident and must be reported unclean.
func TestConfirmRecoverByDB(t *testing.T) {
	store := newFakeStore(1, 4, string(FaultExecType))
	n1, n2, n3, n4 := store.nodes[0], store.nodes[1], store.nodes[2], store.nodes[3]
	e1 := &fakeExecutor{name: "cr-n1", phase: string(RecoverPhaseType), status: string(SuccessStatusType)} // clean (recovered)
	e2 := &fakeExecutor{name: "cr-n2", status: string(RunningStatusType)}                                   // not clean (still recovering)
	e3 := &fakeExecutor{name: "cr-n3", status: ""}                                                          // absent → clean
	e4 := &fakeExecutor{name: "cr-n4", phase: string(InjectPhaseType), status: string(SuccessStatusType)}  // P1-1: inject-phase success = NOT clean
	r := reconciler(store, map[string]*fakeExecutor{n1.UUID: e1, n2.UUID: e2, n3.UUID: e3, n4.UUID: e4})

	unclean := r.ConfirmRecoverByDB("inst-1")
	if len(unclean) != 2 {
		t.Fatalf("want 2 unclean (cr-n2 recovering, cr-n4 inject-phase-resident), got %v", unclean)
	}
	got := map[string]bool{}
	for _, n := range unclean {
		got[n] = true
	}
	if !got["cr-n2"] || !got["cr-n4"] {
		t.Fatalf("want {cr-n2,cr-n4}, got %v", unclean)
	}
	_ = n1
	_ = n3
}

// TestReconcileAbortsWhenCRStillInjecting is the Codex P1-1 regression: a fault node whose CR reports
// status=success while STILL in the inject phase must NOT be marked succeeded — otherwise the strict-
// serial reconciler starts the next node while this fault is still resident (叠加注入). The reconciler
// should instead issue Recover and leave the node running; only after the CR flips to phase=recover does
// the node complete.
func TestReconcileAbortsWhenCRStillInjecting(t *testing.T) {
	store := newFakeStore(1, 2, string(FaultExecType))
	n1, n2 := store.nodes[0], store.nodes[1]
	e1 := &fakeExecutor{name: "cr-n1", phase: "", status: ""}
	e2 := &fakeExecutor{name: "cr-n2", status: ""}
	r := reconciler(store, map[string]*fakeExecutor{n1.UUID: e1, n2.UUID: e2})

	// Tick 1: start n-1.
	if st, _ := r.ReconcileInstance("inst-1"); st != WorkflowRunning {
		t.Fatalf("tick1: want Running, got %s", st)
	}
	if n1.Status != nodeStatusRunning || e2.creates != 0 {
		t.Fatalf("tick1: n1=%s n2.creates=%d, want n1=running n2 not started", n1.Status, e2.creates)
	}

	// Tick 2: n-1 CR reports status=success but phase=inject (inject done, recover NOT started) →
	// n-1 must stay running, instance stays Running, and n-2 must NOT start. A Recover was issued.
	e1.phase = string(InjectPhaseType)
	e1.status = string(SuccessStatusType)
	beforeRecover := e1.recovers
	if st, _ := r.ReconcileInstance("inst-1"); st != WorkflowRunning {
		t.Fatalf("tick2 (inject-phase success): want Running, got %s", st)
	}
	if n1.Status != nodeStatusRunning {
		t.Fatalf("tick2: n1=%s want running (inject-phase success must NOT complete)", n1.Status)
	}
	if e2.creates != 0 {
		t.Fatalf("tick2: n2 started during resident inject (P1-1 regression!), creates=%d", e2.creates)
	}
	if e1.recovers != beforeRecover+1 {
		t.Fatalf("tick2: expected a Recover issued to flip inject→recover, recovers=%d→%d", beforeRecover, e1.recovers)
	}

	// Tick 3: CR now flipped to recover+success → n-1 completes, n-2 starts.
	e1.phase = string(RecoverPhaseType)
	if st, _ := r.ReconcileInstance("inst-1"); st != WorkflowRunning {
		t.Fatalf("tick3: want Running, got %s", st)
	}
	if n1.Status != nodeStatusSucceeded || n2.Status != nodeStatusRunning {
		t.Fatalf("tick3: n1=%s n2=%s, want n1=succeeded n2=running", n1.Status, n2.Status)
	}
}

// TestReconcileCreateFailure: a Create error flips the node failed and fails the instance.
func TestReconcileCreateFailure(t *testing.T) {
	store := newFakeStore(1, 1, string(FaultExecType))
	n1 := store.nodes[0]
	e1 := &fakeExecutor{name: "cr-n1", failCreate: fmt.Errorf("k8s 503")}
	r := reconciler(store, map[string]*fakeExecutor{n1.UUID: e1})

	st, _ := r.ReconcileInstance("inst-1")
	if st != WorkflowFailed {
		t.Fatalf("want Failed on create error, got %s", st)
	}
	if n1.Status != nodeStatusFailed {
		t.Fatalf("want node failed, got %s", n1.Status)
	}
}
