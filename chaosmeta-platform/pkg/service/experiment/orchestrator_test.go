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
	name    string
	status  string // what GetStatus returns once "running"
	creates int
	recovers int
	failCreate error
}

func (e *fakeExecutor) Name() (string, error)                                          { return e.name, nil }
func (e *fakeExecutor) GetStatus(context.Context, string, string) (string, error)     { return e.status, nil }
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

	// Tick 2: n-1 CR now reports clean → complete n-1 (succeeded) + start n-2.
	e1.status = string(SuccessStatusType)
	if st, _ := r.ReconcileInstance("inst-1"); st != WorkflowRunning {
		t.Fatalf("tick2: want Running, got %s", st)
	}
	if n1.Status != nodeStatusSucceeded {
		t.Fatalf("tick2: n1=%s want succeeded", n1.Status)
	}
	if e1.recovers != 1 {
		t.Fatalf("tick2: fault node completion should issue recover, recovers=%d", e1.recovers)
	}
	if n2.Status != nodeStatusRunning || e2.creates != 1 {
		t.Fatalf("tick2: n2=%s creates=%d, want running+started", n2.Status, e2.creates)
	}

	// Tick 3: n-2 clean → instance succeeds.
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

// TestConfirmRecoverByDB: clean/absent fault CRs → empty; a not-clean CR → reported by name.
func TestConfirmRecoverByDB(t *testing.T) {
	store := newFakeStore(1, 3, string(FaultExecType))
	n1, n2, n3 := store.nodes[0], store.nodes[1], store.nodes[2]
	e1 := &fakeExecutor{name: "cr-n1", status: string(SuccessStatusType)} // clean
	e2 := &fakeExecutor{name: "cr-n2", status: string(RunningStatusType)} // not clean (still recovering)
	e3 := &fakeExecutor{name: "cr-n3", status: ""}                        // absent → clean
	r := reconciler(store, map[string]*fakeExecutor{n1.UUID: e1, n2.UUID: e2, n3.UUID: e3})

	unclean := r.ConfirmRecoverByDB("inst-1")
	if len(unclean) != 1 || unclean[0] != "cr-n2" {
		t.Fatalf("want [cr-n2], got %v", unclean)
	}
	_ = n1
	_ = n3
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
