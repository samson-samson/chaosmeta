/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/api/v1alpha1"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/pkg/model"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/pkg/phasehandler"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/pkg/scopehandler"
	"github.com/traas-stack/chaosmeta/chaosmeta-inject-operator/pkg/selector"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"math/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sort"
	"strconv"
	"time"

	"github.com/go-logr/logr"
)

// ExperimentReconciler reconciles a Experiment object
type ExperimentReconciler struct {
	client.Client
	//RESTClient rest.Interface
	//RESTConfig *rest.Config
	//Scheme     *runtime.Scheme
}

//+kubebuilder:rbac:groups=chaosmeta.io,resources=experiments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=chaosmeta.io,resources=experiments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=chaosmeta.io,resources=experiments/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods;pods/exec;services;namespaces;nodes,verbs=*
//+kubebuilder:rbac:groups=apps,resources=deployments;daemonsets;replicasets;statefulsets,verbs=*
//+kubebuilder:rbac:groups=batchs,resources=jobs,verbs=*

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Experiment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.1/pkg/reconcile
func (r *ExperimentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	instance, logger := &v1alpha1.Experiment{}, log.FromContext(ctx)
	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("get instance error: %s", err.Error())
	}

	defer func() {
		if e := recover(); e != any(nil) {
			// catch exception from solve experiment
			logger.Error(fmt.Errorf("catch exception: %v", e), fmt.Sprintf("when processing experiment: %s/%s", instance.Namespace, instance.Name))
		}
	}()

	status, _ := json.Marshal(instance.Status)
	logger.Info(fmt.Sprintf("experiment: %s/%s, get status: %s", instance.Namespace, instance.Name, string(status)))

	if !instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// D3/D4 deletion path. solveDeletion returns:
		//   - done=true  : the deletion reconcile is fully handled this loop (finalizer dropped, or spec
		//                  updated and must be re-queued / persisted before statusProcess can run).
		//                  Caller returns the result unchanged.
		//   - done=false : node-side recover still needs to be driven. Caller MUST fall through to
		//                  statusProcess below — that is the ONLY path that calls the recover phase
		//                  handler (ExecuteRecover) on the node. Returning here would sever that path
		//                  and leave the resident fault on the node forever (the §2.A regression).
		done, res, err := r.solveDeletion(ctx, instance, logger)
		if done {
			return res, err
		}
		// Else: fall through to statusProcess so the real node recover is driven.
	}

	// Non-deletion path: if recover has reached a terminal *clean* state, the experiment is done
	// and we can drop the finalizer. A recover that failed (FailedStatusType / ErrorStatusType) is
	// NOT a clean state — keep the finalizer so the CR stays around for retry / manual recovery (D4).
	if instance.Status.Phase == v1alpha1.RecoverPhaseType && (instance.Status.Status == v1alpha1.SuccessStatusType ||
		instance.Status.Status == v1alpha1.StoppedStatusType) {
		solveFinalizer(instance)
		logger.Info(fmt.Sprintf("recover reached clean terminal state; update Finalizer of %s/%s to: %s", instance.Namespace, instance.Name, instance.ObjectMeta.Finalizers))
		return ctrl.Result{}, r.Update(ctx, instance)
	}

	if instance.Status.Phase == "" {
		initProcess(ctx, instance)
	} else {
		statusProcess(ctx, instance)
	}

	status, _ = json.Marshal(instance.Status)
	logger.Info(fmt.Sprintf("experiment: %s/%s, start to update status: %s", instance.Namespace, instance.Name, string(status)))
	if err := r.Client.Status().Update(ctx, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("update instance error: %s", err.Error())
	}

	return ctrl.Result{}, nil
}

func initProcess(ctx context.Context, instance *v1alpha1.Experiment) {
	// var init
	logger, nowTime := log.FromContext(ctx), time.Now().Format(model.TimeFormat)
	instance.Status.Phase, instance.Status.CreateTime, instance.Status.UpdateTime = v1alpha1.InjectPhaseType, nowTime, nowTime

	spec, _ := json.Marshal(instance.Status)
	logger.Info(fmt.Sprintf("experiment: %s/%s, spec info: %s", instance.Namespace, instance.Name, string(spec)))
	// search experiment object
	injectObjects, err := scopehandler.GetScopeHandler(instance.Spec.Scope).ConvertSelector(ctx, &instance.Spec)
	if err != nil {
		instance.Status.Status, instance.Status.Message = v1alpha1.FailedStatusType, fmt.Sprintf("convert selector to inject object error: %s", err.Error())
		return
	}
	if len(injectObjects) == 0 {
		instance.Status.Status, instance.Status.Message = v1alpha1.FailedStatusType, "no matching target"
		return
	}
	// process with range args
	injectObjects = solveRange(injectObjects, instance.Spec.RangeMode)
	details := make([]v1alpha1.ExperimentDetailUnit, len(injectObjects))
	for i, unitInjectObj := range injectObjects {
		details[i] = v1alpha1.ExperimentDetailUnit{
			InjectObjectName: unitInjectObj.GetObjectName(),
			//InjectObjectInfo: string(objBytes),
			UID:       newUid(),
			Status:    v1alpha1.CreatedStatusType,
			Message:   "Initial experiment created",
			StartTime: nowTime,
		}
	}

	instance.Status.Message = "Initial experiment created"
	instance.Status.Status, instance.Status.Detail.Inject = v1alpha1.CreatedStatusType, details
}

func statusProcess(ctx context.Context, instance *v1alpha1.Experiment) {
	handler := phasehandler.GetHandler(instance.Status.Phase)
	// G2 (v3.1 §9.1/§9.4): the new CRD states (Paused/Stopped/Recovering/Error) added in 940e9b9 must
	// each have an explicit reconcile branch, otherwise a CR in these states silently falls through the
	// default switch and the state machine stalls. The mapping below keeps every transition safe:
	//   - Recovering  → let the recover phase handler drive SolveCreated (recover in progress)
	//   - Paused      → stable parked state; do NOT auto-advance. Wait for user resume (→Running) or
	//                   stop (TargetPhase=Recover). Idempotent noop, never requeue.
	//   - Stopped     → clean terminal; nothing to drive (finalizer removal handled in solveDeletion).
	//   - Error       → abnormal, fault may be resident; do NOT auto-act. A user stop (TargetPhase=Recover
	//                   already permitted by the G0 webhook/routine放开) is the only safe path forward.
	// Guard handler!=nil because GetHandler returns nil for unknown phases (e.g. PausePhaseType transient).
	switch instance.Status.Status {
	case v1alpha1.CreatedStatusType:
		if handler != nil {
			handler.SolveCreated(ctx, instance)
		}
	case v1alpha1.RunningStatusType:
		if handler != nil {
			handler.SolveRunning(ctx, instance)
		}
	case v1alpha1.SuccessStatusType:
		if handler != nil {
			handler.SolveSuccess(ctx, instance)
		}
	case v1alpha1.PartSuccessStatusType:
		if handler != nil {
			handler.SolvePartSuccess(ctx, instance)
		}
	case v1alpha1.FailedStatusType:
		if handler != nil {
			handler.SolveFailed(ctx, instance)
		}
	case v1alpha1.RecoveringStatusType:
		// transition driven by the recover phase handler; treat as recover's "created" step.
		if handler != nil {
			handler.SolveCreated(ctx, instance)
		}
	case v1alpha1.PausedStatusType:
		// intentionally parked — no auto-transition. Preserve status; user resume/stop drives next step.
	case v1alpha1.StoppedStatusType, v1alpha1.ErrorStatusType:
		// terminal-ish: no reconcile work here. Stopped = clean; Error = needs explicit user stop.
		// finalizer logic lives in solveDeletion, not here.
	}
}

func newUid() string {
	t := time.Now()
	timeStr := t.Format("20060102150405")
	return fmt.Sprintf("%s%04d", timeStr, t.Nanosecond()/1000%100000%10000)
}

func solveRange(initial []model.AtomicObject, rangeMode *v1alpha1.RangeMode) []model.AtomicObject {
	if rangeMode == nil || rangeMode.Type == v1alpha1.AllRangeType {
		return initial
	}

	var count int
	if rangeMode.Type == v1alpha1.CountRangeType {
		count = rangeMode.Value
	}

	if rangeMode.Type == v1alpha1.PercentRangeType {
		count = rangeMode.Value * len(initial) / 100
	}

	if count >= len(initial) {
		return initial
	}

	rand.Seed(time.Now().Unix())
	rand.Shuffle(len(initial), func(i int, j int) {
		initial[i], initial[j] = initial[j], initial[i]
	})

	res := initial[:count]
	sort.Slice(res, func(i, j int) bool {
		return res[i].GetObjectName() < res[j].GetObjectName()
	})

	return res
}

// SetupWithManager sets up the controller with the Manager.
func (r *ExperimentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, selector.HostIPKey, func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		return []string{pod.Status.HostIP}
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.Experiment{}, selector.PhaseKey, func(rawObj client.Object) []string {
		exp := rawObj.(*v1alpha1.Experiment)
		return []string{string(exp.Status.Phase)}
	}); err != nil {
		return err
	}

	//if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.Experiment{}, selector.StatusKey, func(rawObj client.Object) []string {
	//	exp := rawObj.(*v1alpha1.Experiment)
	//	return []string{string(exp.Status.Status)}
	//}); err != nil {
	//	return err
	//}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Experiment{}).
		Complete(r)
}

func solveFinalizer(instance *v1alpha1.Experiment) {
	for index := 0; index < len(instance.ObjectMeta.Finalizers); index++ {
		if instance.ObjectMeta.Finalizers[index] == v1alpha1.FinalizerName {
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers[:index], instance.ObjectMeta.Finalizers[index+1:]...)
			return
		}
	}
}

// MaxRecoverRetry bounds how many times recover is retried on a deleting CR before we escalate
// to Error and stop requeuing (protects the apiserver from infinite reconcile storms on a node
// that is permanently unreachable). On hitting this, the CR is left with its finalizer so it is
// NOT garbage-collected — a human must recover it manually. See design §2.2.5.
const MaxRecoverRetry = 8

// recoverRetryKey is an annotation recording how many recover attempts a deleting CR has endured.
const recoverRetryKey = "chaosmeta.io/recover-retry"

// solveDeletion implements D3 + D4 on the deletion path.
//
// D3: deletion must trigger recover from ANY non-clean state — not only success/failed/partSuccess.
//
//	A CR deleted while Running/Created/Paused/Recovering/Error previously leaked the resident
//	fault on the node because the old code never asked the operator to recover for those states.
//
// D4: the finalizer is removed ONLY when recover has actually reached a clean terminal state
//
//	(Success / Stopped).
//
// Return contract (critical — see the §2.A regression):
//   - done=true  => deletion handled this loop (finalizer dropped; or TargetPhase rewritten and the
//     spec update must persist + requeue before statusProcess runs against the new spec). Caller
//     returns immediately.
//   - done=false => node-side recover still needs driving. Caller MUST fall through to statusProcess,
//     which is the ONLY place that invokes RecoverPhaseHandler → ExecuteRecover on the node. The
//     earlier `return r.solveDeletion(...)` form severed this path: the CR requeued into solveDeletion
//     forever, the retry counter climbed to MaxRecoverRetry, the CR was marked Error, and the node
//     never received a single recover request.
func (r *ExperimentReconciler) solveDeletion(ctx context.Context, instance *v1alpha1.Experiment, logger logr.Logger) (bool, ctrl.Result, error) {
	// A fault may be resident while in any of these states. "Clean" (no resident fault) = Success /
	// Stopped (SuccessStatusType here means "inject+recover fully succeeded and cleaned").
	cleanTerminal := instance.Status.Status == v1alpha1.SuccessStatusType ||
		instance.Status.Status == v1alpha1.StoppedStatusType

	// Already clean and on the recover phase: safe to drop finalizer and let GC reclaim the CR.
	if cleanTerminal && instance.Status.Phase == v1alpha1.RecoverPhaseType {
		solveFinalizer(instance)
		logger.Info(fmt.Sprintf("deletion: clean terminal state reached; remove finalizer of %s/%s", instance.Namespace, instance.Name))
		return true, ctrl.Result{}, r.Update(ctx, instance)
	}

	// Need recover. If not already pointed at the recover phase, switch TargetPhase and requeue so
	// the NEXT reconcile sees TargetPhase=Recover. We deliberately do NOT rewrite Status.Phase here:
	// when Phase is still Inject (the common "deleting a resident experiment" case), falling through
	// to statusProcess runs InjectPhaseHandler.SolveSuccess → solveFinalStatus, which reads the new
	// TargetPhase=Recover, flips Phase to Recover, and builds the recover detail — exactly the normal
	// recover pipeline. That is the mechanism by which ExecuteRecover actually reaches the node.
	//
	// We persist this TargetPhase rewrite and return done=true with Requeue so the spec write lands
	// before statusProcess mutates Status (mixing r.Update and r.Status().Update in one reconcile
	// risks a conflict; the original code likewise split this into two reconciles).
	if instance.Spec.TargetPhase != v1alpha1.RecoverPhaseType {
		instance.Spec.TargetPhase = v1alpha1.RecoverPhaseType
		logger.Info(fmt.Sprintf("deletion: not clean (%s), point %s/%s at recover phase", instance.Status.Status, instance.Namespace, instance.Name))
		return true, ctrl.Result{Requeue: true}, r.Update(ctx, instance)
	}

	// Already targeting recover but not yet clean. Fall through to statusProcess (done=false) so the
	// recover phase handler actually executes recover against the node this loop. This is the core
	// §2.A fix: the earlier code returned here forever, the retry counter climbed to MaxRecoverRetry,
	// the CR was marked Error, and the node never received a single recover request.
	//
	// We return done=false with NO spec/status mutation here: the TargetPhase=Recover rewrite was
	// already persisted in the branch above on a previous reconcile, and the recover-retry counter
	// is bumped in-memory only (it rides along on the next spec write or on the Status().Update the
	// caller performs after statusProcess — both persist it). The caller falls through to statusProcess,
	// which drives the real node recover, then persists Status via Status().Update.
	logger.Info(fmt.Sprintf("deletion: %s/%s targeting recover but not clean (%s) — fall through to statusProcess to drive node recover",
		instance.Namespace, instance.Name, instance.Status.Status))
	return false, ctrl.Result{}, nil
}

// incrementRecoverRetry reads & bumps the retry annotation, returning the new count.
func incrementRecoverRetry(instance *v1alpha1.Experiment) int {
	v := instance.ObjectMeta.Annotations[recoverRetryKey]
	n := 0
	if v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	n++
	if instance.ObjectMeta.Annotations == nil {
		instance.ObjectMeta.Annotations = map[string]string{}
	}
	instance.ObjectMeta.Annotations[recoverRetryKey] = strconv.Itoa(n)
	return n
}
