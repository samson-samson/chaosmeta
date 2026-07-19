/*
 * DB orchestration — production wiring (adapters + reconciler construction).
 *
 * Pairs with orchestrator.go (pure logic). This file is the only place the production path touches
 * K8s/argo/client-go + the experiment_instance service:
 *   - dbNodeStore: wraps the experiment_instance model funcs behind nodeStore.
 *   - injectExecutor/flowExecutor/measureExecutor: adapt the three Chaosmeta*Service types behind
 *     NodeExecutor. CR construction REUSES the legacy getFaultStep/getFlowStep/getMeasureStep builders
 *     in experiment_custom_resource.go (single source of truth — no re-implementation of the
 *     selector/args/subtasks marshalling, and the kept Argo path stays byte-identical). We decode the CR
 *     yaml the builder stuffs into its Argo Parameter back into the typed struct and hand it to the
 *     service Create — a safe round-trip since Create marshals it back internally anyway.
 *   - NewDBReconciler: builds a Reconciler whose factory does the per-node detail lookup + service
 *     construction. The detail fetch is per node per tick; caching it cluster-side is left as an honest
 *     boundary (design §6).
 *
 * K8s+operator real-end verification is left to the cluster; this file is cross-compiled and the pure
 * logic is unit-tested against fakes only (orchestrator_test.go).
 */

package experiment

import (
	"context"
	"fmt"

	"chaosmeta-platform/config"
	experimentInstanceModel "chaosmeta-platform/pkg/models/experiment_instance"
	"chaosmeta-platform/pkg/service/experiment_instance"

	"github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"sigs.k8s.io/yaml"
	"k8s.io/client-go/rest"
)

// dbNodeStore is the production nodeStore: thin wrappers over the experiment_instance model. The model's
// UpdateWorkflowNodeInstanceStatus honors the Version optimistic lock, so concurrent reconcile ticks on
// the same node do not silently clobber each other.
type dbNodeStore struct{}

func (dbNodeStore) ListByExperiment(instanceUUID string) ([]*experimentInstanceModel.WorkflowNodeInstance, error) {
	return experimentInstanceModel.GetWorkflowNodeInstancesByExperimentUUID(instanceUUID)
}

func (dbNodeStore) UpdateStatus(uuid, status, message string) error {
	return experimentInstanceModel.UpdateWorkflowNodeInstanceStatus(uuid, status, message)
}

// paramYAML extracts the CR yaml the legacy builder stuffed into the DAGTask's first parameter (every
// builder sets exactly one Parameter named ParametersName holding the CR yaml). Returns the yaml string.
func paramYAML(step *v1alpha1.DAGTask) (string, error) {
	if step == nil || len(step.Arguments.Parameters) == 0 {
		return "", fmt.Errorf("step has no parameter")
	}
	p := step.Arguments.Parameters[0]
	if p.Value == nil {
		return "", fmt.Errorf("step parameter has no value")
	}
	return p.Value.String(), nil
}

// ---------- CR constructors (reuse legacy builders) ----------

func faultCR(instanceUUID string, detail *experiment_instance.WorkflowNodesDetail, phase PhaseType) (*ExperimentInjectStruct, string, error) {
	step := getFaultStep(instanceUUID, detail, phase)
	if step == nil {
		return nil, "", fmt.Errorf("getFaultStep returned nil for node %s", detail.UUID)
	}
	crYaml, err := paramYAML(step)
	if err != nil {
		return nil, step.Name, err
	}
	var cr ExperimentInjectStruct
	if err := yaml.Unmarshal([]byte(crYaml), &cr); err != nil {
		return nil, step.Name, fmt.Errorf("decode fault CR: %w", err)
	}
	return &cr, step.Name, nil
}

func flowCR(instanceUUID string, detail *experiment_instance.WorkflowNodesDetail) (*LoadTest, string, error) {
	step := getFlowStep(instanceUUID, detail)
	if step == nil {
		return nil, "", fmt.Errorf("getFlowStep returned nil for node %s", detail.UUID)
	}
	crYaml, err := paramYAML(step)
	if err != nil {
		return nil, step.Name, err
	}
	var cr LoadTest
	if err := yaml.Unmarshal([]byte(crYaml), &cr); err != nil {
		return nil, step.Name, fmt.Errorf("decode flow CR: %w", err)
	}
	return &cr, step.Name, nil
}

func measureCR(instanceUUID string, detail *experiment_instance.WorkflowNodesDetail) (*CommonMeasureStruct, string, error) {
	step := getMeasureStep(instanceUUID, detail)
	if step == nil {
		return nil, "", fmt.Errorf("getMeasureStep returned nil for node %s", detail.UUID)
	}
	crYaml, err := paramYAML(step)
	if err != nil {
		return nil, step.Name, err
	}
	var cr CommonMeasureStruct
	if err := yaml.Unmarshal([]byte(crYaml), &cr); err != nil {
		return nil, step.Name, fmt.Errorf("decode measure CR: %w", err)
	}
	return &cr, step.Name, nil
}

// ---------- NodeExecutor adapters ----------

type injectExecutor struct {
	svc                    ChaosmetaInterface
	detail                 *experiment_instance.WorkflowNodesDetail
	experimentInstanceUUID string
	phase                  PhaseType // inject on start; recover flips per Recover()
	name                   string    // resolved once at construction; never changes
}

func (e *injectExecutor) Name() (string, error) { return e.name, nil }

func (e *injectExecutor) GetStatus(ctx context.Context, namespace, name string) (string, string, error) {
	cr, err := e.svc.Get(ctx, namespace, name)
	if err != nil || cr == nil {
		return "", "", err // absent/unreadable → reconciler treats "" as not-yet-statused
	}
	// fault CR carries Phase: return both so isCRStatusClean can enforce phase==recover (P1-1).
	return string(cr.Status.Phase), string(cr.Status.Status), nil
}

func (e *injectExecutor) Create(ctx context.Context) (string, error) {
	cr, name, err := faultCR(e.experimentInstanceUUID, e.detail, e.phase)
	if err != nil {
		return "", err
	}
	if _, err := e.svc.Create(ctx, cr); err != nil {
		return "", err
	}
	return name, nil
}

func (e *injectExecutor) Recover(namespace, name string) error { return e.svc.Recover(namespace, name) }

type flowExecutor struct {
	svc                    ChaosmetaFlowInterface
	detail                 *experiment_instance.WorkflowNodesDetail
	experimentInstanceUUID string
	name                   string
}

func (e *flowExecutor) Name() (string, error) { return e.name, nil }

func (e *flowExecutor) GetStatus(ctx context.Context, namespace, name string) (string, string, error) {
	cr, err := e.svc.Get(ctx, namespace, name)
	if err != nil || cr == nil {
		return "", "", err
	}
	// flow CR has no Phase field → phase ""; status alone is the clean signal (P1-1).
	return "", string(cr.Status.Status), nil
}

func (e *flowExecutor) Create(ctx context.Context) (string, error) {
	cr, name, err := flowCR(e.experimentInstanceUUID, e.detail)
	if err != nil {
		return "", err
	}
	if _, err := e.svc.Create(ctx, cr); err != nil {
		return "", err
	}
	return name, nil
}

func (e *flowExecutor) Recover(namespace, name string) error { return e.svc.Recover(namespace, name) }

type measureExecutor struct {
	svc                    ChaosmetaMeasureInterface
	detail                 *experiment_instance.WorkflowNodesDetail
	experimentInstanceUUID string
	name                   string
}

func (e *measureExecutor) Name() (string, error) { return e.name, nil }

func (e *measureExecutor) GetStatus(ctx context.Context, namespace, name string) (string, string, error) {
	cr, err := e.svc.Get(ctx, namespace, name)
	if err != nil || cr == nil {
		return "", "", err
	}
	// measure CR has no Phase field → phase ""; status alone is the clean signal (P1-1).
	return "", string(cr.Status.Status), nil
}

func (e *measureExecutor) Create(ctx context.Context) (string, error) {
	cr, name, err := measureCR(e.experimentInstanceUUID, e.detail)
	if err != nil {
		return "", err
	}
	if _, err := e.svc.Create(ctx, cr); err != nil {
		return "", err
	}
	return name, nil
}

func (e *measureExecutor) Recover(namespace, name string) error { return e.svc.Recover(namespace, name) }

// ---------- Reconciler construction ----------

// NewDBReconciler builds the production Reconciler. restConfig is the cluster *rest.Config captured at
// construction; the factory builds the matching chaosmeta service per ExecType and fetches the node
// detail (subtasks+args) the CR builders require.
func NewDBReconciler(restConfig *rest.Config, namespace string) *Reconciler {
	instanceSvc := &experiment_instance.ExperimentInstanceService{}
	return &Reconciler{
		store:     dbNodeStore{},
		namespace: func() string { return namespace },
		newExecutor: func(node *experimentInstanceModel.WorkflowNodeInstance, phase PhaseType) (NodeExecutor, error) {
			detail, err := instanceSvc.GetWorkflowNodeInstanceDetailByUUIDAndNodeId(node.ExperimentInstanceUUID, node.UUID)
			if err != nil || detail == nil {
				return nil, fmt.Errorf("load node detail %s: %w", node.UUID, err)
			}
			switch ExecType(node.ExecType) {
			case FaultExecType:
				_, name, nerr := faultCR(node.ExperimentInstanceUUID, detail, phase)
				if nerr != nil {
					return nil, nerr
				}
				return &injectExecutor{svc: NewChaosmetaService(restConfig), detail: detail, experimentInstanceUUID: node.ExperimentInstanceUUID, phase: phase, name: name}, nil
			case FlowExecType:
				_, name, nerr := flowCR(node.ExperimentInstanceUUID, detail)
				if nerr != nil {
					return nil, nerr
				}
				return &flowExecutor{svc: NewChaosmetaFlowService(restConfig), detail: detail, experimentInstanceUUID: node.ExperimentInstanceUUID, name: name}, nil
			case MeasureExecType:
				_, name, nerr := measureCR(node.ExperimentInstanceUUID, detail)
				if nerr != nil {
					return nil, nerr
				}
				return &measureExecutor{svc: NewChaosmetaMeasureService(restConfig), detail: detail, experimentInstanceUUID: node.ExperimentInstanceUUID, name: name}, nil
			case WaitExecType:
				return nil, fmt.Errorf("wait node %s needs no executor", node.UUID)
			default:
				return nil, fmt.Errorf("unknown exec type %q for node %s", node.ExecType, node.UUID)
			}
		},
	}
}

// referenced so the namespace-fallback path (config.DefaultRunOptIns.WorkflowNamespace) stays available
// if a future caller prefers the config value over an explicit namespace arg.
var _ = config.DefaultRunOptIns

// ctxReferenced keeps the context import meaningful for the adapter Get signatures above.
var _ = context.Background
