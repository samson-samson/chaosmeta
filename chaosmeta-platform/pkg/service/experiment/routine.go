/*
 * Copyright 2022-2023 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package experiment

import (
	"chaosmeta-platform/config"
	"chaosmeta-platform/pkg/models/experiment"
	experimentInstanceModel "chaosmeta-platform/pkg/models/experiment_instance"
	"chaosmeta-platform/pkg/service/cluster"
	"chaosmeta-platform/pkg/service/experiment_instance"
	"chaosmeta-platform/pkg/service/user"
	"chaosmeta-platform/util/log"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/robfig/cron"
	"gopkg.in/yaml.v2"
	"k8s.io/client-go/rest"
	"time"
)

const (
	DefaultFormat = "2006-01-02 15:04:05"

	WorkflowPending   = "Pending" // pending some set-up - rarely used
	WorkflowRunning   = "Running" // any node has started; pods might not be running yet, the workflow maybe suspended too
	WorkflowSucceeded = "Succeeded"
	WorkflowFailed    = "Failed" // it maybe that the workflow was terminated
	WorkflowError     = "Error"
)

type ExperimentRoutine struct {
	context   context.Context
	localCron *cron.Cron
}

func convertToWorkflowNodesDetail(node *WorkflowNode, workflowNodesDetail *experiment_instance.WorkflowNodesDetail) {
	if node == nil || workflowNodesDetail == nil {
		return
	}
	if node.FaultRange != nil {
		workflowNodesDetail.Subtasks = &experimentInstanceModel.FaultRangeInstance{
			TargetName:      node.FaultRange.TargetName,
			TargetIP:        node.FaultRange.TargetIP,
			TargetHostname:  node.FaultRange.TargetHostname,
			TargetLabel:     node.FaultRange.TargetLabel,
			TargetApp:       node.FaultRange.TargetApp,
			TargetNamespace: node.FaultRange.TargetNamespace,
			RangeType:       node.FaultRange.RangeType,
		}
	}
	if node.FlowRange != nil {
		workflowNodesDetail.FlowSubtasks = &experimentInstanceModel.FlowRangeInstance{
			Source:      node.FlowRange.Source,
			Parallelism: node.FlowRange.Parallelism,
			Duration:    node.FlowRange.Duration,
			FlowType:    node.FlowRange.FlowType,
		}
	}
	if node.MeasureRange != nil {
		workflowNodesDetail.MeasureSubtasks = &experimentInstanceModel.MeasureRangeInstance{
			JudgeValue:   node.MeasureRange.JudgeValue,
			JudgeType:    node.MeasureRange.JudgeType,
			FailedCount:  node.MeasureRange.FailedCount,
			SuccessCount: node.MeasureRange.SuccessCount,
			Interval:     node.MeasureRange.Interval,
			Duration:     node.MeasureRange.Duration,
			MeasureType:  node.MeasureRange.MeasureType,
		}
	}
}

func convertToExperimentInstance(experiment *ExperimentGet, status string) *experiment_instance.ExperimentInstance {
	experimentInstance := &experiment_instance.ExperimentInstance{
		ExperimentInstanceInfo: experiment_instance.ExperimentInstanceInfo{
			UUID:        experiment.UUID,
			Name:        experiment.Name,
			Description: experiment.Description,
			Creator:     experiment.Creator,
			NamespaceId: experiment.NamespaceID,
			Status:      status,
		},
		Labels: getLabelIdsFromLabelGet(experiment.Labels),
	}

	for _, node := range experiment.WorkflowNodes {
		workflowNodeDetail := &experiment_instance.WorkflowNodesDetail{
			WorkflowNodesInfo: experiment_instance.WorkflowNodesInfo{
				UUID:     node.UUID,
				Name:     node.Name,
				Row:      node.Row,
				Column:   node.Column,
				Duration: node.Duration,
				ScopeId:  node.ScopeId,
				TargetId: node.TargetId,
				ExecType: node.ExecType,
				ExecName: node.ExecName,
				ExecId:   node.ExecID,
			},
			Subtasks: &experimentInstanceModel.FaultRangeInstance{
				WorkflowNodeInstanceUUID: node.UUID,
			},
		}
		convertToWorkflowNodesDetail(node, workflowNodeDetail)
		for _, arg := range node.ArgsValue {
			workflowNodeDetail.ArgsValues = append(workflowNodeDetail.ArgsValues, experiment_instance.ArgsValue{ArgsId: arg.ArgsID, Value: arg.Value})
		}
		experimentInstance.WorkflowNodes = append(experimentInstance.WorkflowNodes, workflowNodeDetail)
	}

	experimentInstanceBytes, _ := json.Marshal(experimentInstance)
	log.Error("convertToExperimentInstance:", string(experimentInstanceBytes))
	return experimentInstance
}

func StartExperiment(experimentID string, creatorName string) error {
	experimentService := ExperimentService{}
	experimentGet, err := experimentService.GetExperimentByUUID(experimentID)
	if err != nil || experimentGet == nil {
		return fmt.Errorf("error %v", err)
	}

	experimentInstance := convertToExperimentInstance(experimentGet, string(experimentInstanceModel.Running))
	if creatorName != "" {
		creatorId, err := user.GetIdByName(creatorName)
		if err != nil {
			log.Error(err)
			return err
		}
		experimentInstance.Creator = creatorId
	}
	experimentInstanceService := experiment_instance.ExperimentInstanceService{}
	experimentInstanceId, err := experimentInstanceService.CreateExperimentInstance(experimentInstance, WorkflowPending)
	if err != nil {
		return err
	}

	// v5 DB orchestration (see docs/design/fi-db-orchestration.md): the experiment is no longer driven
	// by an Argo Workflow CR. The instance + its workflow_node_instance rows were created above by
	// CreateExperimentInstance; reconciling now starts the first (strict-serial) node synchronously,
	// and the background ticker (ReconcileRunningInstances) advances running nodes to succeeded/failed.
	restConfig, err := getRunRestConfig()
	if err != nil {
		return err
	}
	status, rerr := NewDBReconciler(restConfig, config.DefaultRunOptIns.WorkflowNamespace).ReconcileInstance(experimentInstanceId)
	if rerr != nil {
		log.Error("reconcile start error:", rerr)
		return rerr
	}
	// Persist the derived instance status so the UI/ticker see it immediately.
	return experimentInstanceModel.UpdateExperimentInstanceStatus(experimentInstanceId, status, "")
}

// getRunRestConfig returns the cluster rest.Config used to build chaosmeta clients. Centralized so the
// start/stop/reconcile paths share one GetRestConfig call and one error-handling shape.
func getRunRestConfig() (*rest.Config, error) {
	clusterService := cluster.ClusterService{}
	_, restConfig, err := clusterService.GetRestConfig(context.Background(), config.DefaultRunOptIns.RunMode.Int())
	return restConfig, err
}

func getInjectMessage(node v1alpha1.NodeStatus) string {
	clusterService := cluster.ClusterService{}
	_, restConfig, err := clusterService.GetRestConfig(context.Background(), config.DefaultRunOptIns.RunMode.Int())
	if err != nil {
		log.Error(err)
		return ""
	}
	injectType, isInject := getInjectSecondField(node.DisplayName)
	var statusData []byte
	if !isInject {
		return node.Message
	}
	switch injectType {
	case string(FaultExecType):
		chaosmetaService := NewChaosmetaService(restConfig)
		experimentInject, err := chaosmetaService.Get(context.Background(), config.DefaultRunOptIns.WorkflowNamespace, node.DisplayName)
		if err != nil {
			log.Error("fault CR get failed, err:", err)
			return ""
		}
		statusData, err = yaml.Marshal(&experimentInject.Status)
		if err != nil {
			log.Errorf("fault CR生成status yaml失败:%v", err)
			return ""
		}

	case string(FlowExecType):
		chaosmetaService := NewChaosmetaFlowService(restConfig)
		experimentFlow, err := chaosmetaService.Get(context.Background(), config.DefaultRunOptIns.WorkflowNamespace, node.DisplayName)
		if err != nil {
			log.Error("flow CR get failed, err:", err)
			return ""
		}
		statusData, err = yaml.Marshal(&experimentFlow.Status)
		if err != nil {
			log.Errorf("flow CR生成status yaml失败:%v", err)
			return ""
		}
	case string(MeasureExecType):
		chaosmetaService := NewChaosmetaMeasureService(restConfig)
		experimentMeasure, err := chaosmetaService.Get(context.Background(), config.DefaultRunOptIns.WorkflowNamespace, node.DisplayName)
		if err != nil {
			log.Error("measure CR get failed, err:", err)
			return ""
		}
		statusData, err = yaml.Marshal(&experimentMeasure.Status)
		if err != nil {
			log.Errorf("measure CR生成status yaml失败:%v", err)
			return ""
		}
	}
	return string(statusData)
}

// Deprecated: DB orchestration (v5) recovers via Reconciler.StopInstanceByDB / completeNode reading
// the DB node rows; this Argo-NodeStatus-driven recover is no longer on the live path.
func injectRecoverByArgo(node v1alpha1.NodeStatus, experimentStatus *string, restConfig *rest.Config) error {
	injectType, isInject := getInjectSecondField(node.DisplayName)
	if isInject {
		nodeId, err := getNodeIDFromStepName(node.DisplayName)
		if err != nil {
			log.Error(err)
			return err
		}
		if node.Phase == v1alpha1.NodeFailed || node.Phase == v1alpha1.NodeError {
			*experimentStatus = string(v1alpha1.WorkflowFailed)

			if err := experimentInstanceModel.UpdateWorkflowNodeInstanceStatus(nodeId, string(node.Phase), getInjectMessage(node)); err != nil {
				log.Error(err)
			}
			return err
		}
		switch injectType {
		case string(FaultExecType):
			chaosmetaService := NewChaosmetaService(restConfig)
			if err := chaosmetaService.Recover(config.DefaultRunOptIns.WorkflowNamespace, node.DisplayName); err != nil {
				log.Error("fault CR recover failed, err:", err)
				return err
			}
		case string(FlowExecType):
			chaosmetaService := NewChaosmetaFlowService(restConfig)
			if err := chaosmetaService.Recover(config.DefaultRunOptIns.WorkflowNamespace, node.DisplayName); err != nil {
				log.Error("flow CR recover failed, err:", err)
				return err
			}
		case string(MeasureExecType):
			chaosmetaService := NewChaosmetaMeasureService(restConfig)
			if err := chaosmetaService.Recover(config.DefaultRunOptIns.WorkflowNamespace, node.DisplayName); err != nil {
				log.Error("measure CR recover failed, err:", err)
				return err
			}
		}
		if err := experimentInstanceModel.UpdateWorkflowNodeInstanceStatus(nodeId, WorkflowSucceeded, getInjectMessage(node)); err != nil {
			log.Error(err)
			return err
		}

		time.AfterFunc(30*time.Second, func() {
			if err := experimentInstanceModel.UpdateWorkflowNodeInstanceMessage(nodeId, getInjectMessage(node)); err != nil {
				log.Error(err)
			}
		})
	}
	return nil
}

func stopExperiment(experimentInstanceID string, experimentStatus *string, tolerateFailure bool) error {
	// Already-ended instance (succeeded) — nothing to stop. Kept from the Argo path so StopExperiment's
	// callers keep the same "experiment has ended" contract.
	instanceInfo, err := experimentInstanceModel.GetExperimentInstanceByUUID(experimentInstanceID)
	if err != nil || instanceInfo == nil {
		return fmt.Errorf("can not find experimentInstance")
	}
	if instanceInfo.Status == WorkflowSucceeded {
		return errors.New("experiment has ended")
	}

	// v5 DB orchestration: recover is driven from the DB node rows, not from an Argo Workflow's
	// NodeStatus. StopInstanceByDB issues Recover on every resident (running/succeeded) node and marks
	// them stopped. tolerateFailure mirrors the legacy "best-effort vs hard-fail" knob.
	restConfig, err := getRunRestConfig()
	if err != nil {
		if !tolerateFailure {
			return err
		}
		log.Error("stopExperiment: get restConfig error:", err)
		return nil
	}
	if err := NewDBReconciler(restConfig, config.DefaultRunOptIns.WorkflowNamespace).StopInstanceByDB(experimentInstanceID); err != nil {
		if !tolerateFailure {
			return err
		}
		log.Error("stopExperiment: StopInstanceByDB error:", err)
	}
	*experimentStatus = WorkflowSucceeded
	return nil
}

func StopExperiment(experimentInstanceID string, tolerateFailure bool) error {
	experimentInstanceInfo, err := experimentInstanceModel.GetExperimentInstanceByUUID(experimentInstanceID)
	if err != nil || experimentInstanceInfo == nil {
		return fmt.Errorf("can not find experimentInstance")
	}
	var experimentStatus = WorkflowSucceeded
	if err := stopExperiment(experimentInstanceID, &experimentStatus, tolerateFailure); err != nil {
		log.Error("stopExperiment error:", err)
	}

	// D9 fix: phase 2 — confirm chaosmetad actually recovered on every node, instead of trusting the
	// Argo shutdown state alone. Poll each inject CR's status until it reaches a clean terminal state
	// (success/stopped) or the confirm timeout elapses. If any node is not clean, escalate to Error and
	// record the un-cleaned nodes so the UI can alert — never silently claim "stopped" with residue.
	uncleanNodes := confirmRecoverCompleted(experimentInstanceID, 60*time.Second)
	if len(uncleanNodes) > 0 {
		experimentStatus = WorkflowError
		msg := fmt.Sprintf("stop incomplete, nodes not confirmed clean: %v", uncleanNodes)
		experimentInstanceInfo.Status = experimentStatus
		experimentInstanceInfo.Message = msg
		log.Error(msg)
		// Persist a log line so the front-end / post-mortem sees the escalation.
		_ = persistStopLog(experimentInstanceID, experimentInstanceInfo.ExperimentUUID, "error", "recover", msg)
	} else {
		experimentInstanceInfo.Status = experimentStatus
	}

	return experimentInstanceModel.UpdateExperimentInstance(experimentInstanceInfo)
}

// confirmRecoverCompleted is the D9 backstop that gives the "stop" operation its safety guarantee:
// before we claim stop succeeded, confirm every fault node's chaosmeta CR actually reached a clean
// terminal state (absent counts as cleaned — it was GC'd after recover). Returns the names (== CR
// names) of nodes NOT confirmed clean (empty = all clean).
//
// v5 DB orchestration: it scans the DB node rows + reverse-lookups the chaosmeta CRs (see
// ConfirmRecoverByDB), NOT Argo Workflow.Status.Nodes. The `timeout` keeps the legacy contract — poll
// until clean or the deadline, so chaosmetad has time to finish the async recover flip.
func confirmRecoverCompleted(experimentInstanceID string, timeout time.Duration) []string {
	restConfig, err := getRunRestConfig()
	if err != nil {
		log.Error("confirmRecoverCompleted: get restConfig error:", err)
		return []string{"<rest-config-unavailable>"}
	}
	rec := NewDBReconciler(restConfig, config.DefaultRunOptIns.WorkflowNamespace)
	deadline := time.Now().Add(timeout)
	for {
		unclean := rec.ConfirmRecoverByDB(experimentInstanceID)
		if len(unclean) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return unclean
		}
		time.Sleep(2 * time.Second)
	}
}

// persistStopLog writes a single log line to the persistence store (D11). Best-effort: a DB error
// degrades to a log.Error and must never break the stop flow.
func persistStopLog(experimentInstanceID, experimentUUID, level, phase, message string) error {
	l := &experimentInstanceModel.ExperimentInstanceLog{
		ExperimentUUID:         experimentUUID,
		ExperimentInstanceUUID: experimentInstanceID,
		Level:                  level,
		Phase:                  phase,
		Message:                message,
	}
	if err := experimentInstanceModel.CreateExperimentInstanceLog(l); err != nil {
		log.Error("persistStopLog error:", err)
		return err
	}
	return nil
}

func UserStopExperiment(experimentInstanceID string) error {
	experimentInstanceInfo, err := experimentInstanceModel.GetExperimentInstanceByUUID(experimentInstanceID)
	if err != nil || experimentInstanceInfo == nil {
		return fmt.Errorf("can not find experimentInstance")
	}
	// G0 (v3.1 §9.1): a clean terminal (Succeeded/Failed) is genuinely done — refuse re-stop.
	// BUT WorkflowError means "stop incomplete, nodes not confirmed clean" (see StopExperiment → confirmRecoverCompleted):
	// the fault may STILL be resident, so Error must be re-stoppable to let the operator issue another recover.
	// This unblocks the Task-2 hard requirement: stop must recover from ANY state (incl. Error) back to clean.
	if experimentInstanceInfo.Status == WorkflowSucceeded || experimentInstanceInfo.Status == WorkflowFailed {
		return errors.New("experiment is over")
	}
	return StopExperiment(experimentInstanceID, false)
}

func (e *ExperimentRoutine) DealOnceExperiment() {
	_, experiments, err := experiment.ListExperimentsByScheduleTypeAndStatus(experiment.OnceMode, experiment.ToBeExecuted)
	if err != nil {
		log.Error(err)
		return
	}

	for _, experimentGet := range experiments {
		nextExec, _ := time.Parse(DefaultFormat, experimentGet.ScheduleRule)
		timeNow, _ := time.Parse(DefaultFormat, time.Now().Format(DefaultFormat))
		if timeNow.After(nextExec) {
			experimentGet.LastInstance = timeNow.Format(TimeLayout)
			log.Info(experimentGet.UUID, "next exec time", experimentGet.NextExec)
			if err := experiment.UpdateExperiment(experimentGet); err != nil {
				log.Error(err)
				continue
			}

			log.Error("create an experiment")
			if err := StartExperiment(experimentGet.UUID, ""); err != nil {
				log.Error(err)
				continue
			}
			experimentGet.Status = experiment.Executed
			if err := experiment.UpdateExperiment(experimentGet); err != nil {
				log.Error(err)
				continue
			}
		} else {
			continue
		}
	}

}

func (e *ExperimentRoutine) DealCronExperiment() {
	_, experiments, err := experiment.ListExperimentsByScheduleTypeAndStatus(experiment.CronMode, experiment.ToBeExecuted)
	if err != nil {
		log.Error(err)
		return
	}
	for _, experimentGet := range experiments {
		cronExpr, err := cron.Parse(experimentGet.ScheduleRule)
		if err != nil {
			continue
		}
		now := time.Now().Add(time.Minute)
		if experimentGet.NextExec.IsZero() {
			experimentGet.NextExec = cronExpr.Next(now)
			if err := experiment.UpdateExperiment(experimentGet); err != nil {
				log.Error(err)
			}
			continue
		}

		if time.Now().After(experimentGet.NextExec) {
			experimentGet.Status = experiment.Executed
			experimentGet.NextExec = cronExpr.Next(now)
			experimentGet.LastInstance = time.Now().Format(TimeLayout)
			log.Info(experimentGet.UUID, "next exec time", experimentGet.NextExec)
			if err := experiment.UpdateExperiment(experimentGet); err != nil {
				log.Error(err)
				continue
			}

			if err := StartExperiment(experimentGet.UUID, ""); err != nil {
				log.Error(err)
			}

			experimentGet.Status = experiment.ToBeExecuted
			if err := experiment.UpdateExperiment(experimentGet); err != nil {
				log.Error(err)
				continue
			}
		}
	}
}

func (e *ExperimentRoutine) syncExperimentStatusByWorkflow(workflow v1alpha1.Workflow) error {
	log.Debug("syncExperimentStatus.Name:", workflow.Name, "workflow.Status", workflow.Status)
	experimentInstanceId, err := getExperimentInstanceIdFromWorkflowName(workflow.Name)
	if err != nil {
		log.Error(err)
		return err
	}

	if err := experimentInstanceModel.UpdateExperimentInstanceStatus(experimentInstanceId, string(workflow.Status.Phase), workflow.Status.Message); err != nil {
		log.Error("UpdateExperimentInstanceStatus err:", err)
		return err
	}

	for _, node := range workflow.Status.Nodes {
		if node.TemplateName == string(ExperimentInject) || node.TemplateName == string(ExperimentInjecFault) {
			nodeId, err := getNodeIDFromStepName(node.DisplayName)
			if err != nil {
				log.Error("getExperimentUUIDAndNodeIDFromStepName:", err)
				continue
			}
			if node.Phase == v1alpha1.NodeFailed || node.Phase == v1alpha1.NodeError {
				return StopExperiment(experimentInstanceId, true)
			}

			getInjectMessage(node)
			if err := experimentInstanceModel.UpdateWorkflowNodeInstanceStatus(nodeId, string(node.Phase), getInjectMessage(node)); err != nil {
				log.Error("UpdateWorkflowNodeInstanceStatus", err)
				continue
			}
		}
	}
	return nil
}

// SyncExperimentsStatus is the legacy Argo-era status sync. Deprecated for the DB-orchestrated path:
// instance status is now driven by ReconcileRunningInstances (which advances DB node rows directly),
// so this Argo-Workflow-status poller is no longer the source of truth. Kept (not deleted) to limit
// blast radius and to serve any still-running Argo instances created before the v5 cutover; not
// registered for new clusters once the DB ticker is the only driver.
//
// Deprecated: use ReconcileRunningInstances.
func (e *ExperimentRoutine) SyncExperimentsStatus() {
	clusterService := cluster.ClusterService{}
	_, restConfig, err := clusterService.GetRestConfig(context.Background(), config.DefaultRunOptIns.RunMode.Int())
	if err != nil {
		log.Error(err)
		return
	}

	argoWorkFlowCtl, err := NewArgoWorkFlowService(restConfig, config.DefaultRunOptIns.ArgoWorkflowNamespace)
	pendingArgos, finishArgos, err := argoWorkFlowCtl.ListPendingAndFinishWorkflows()
	if err != nil {
		log.Error(err)
		return
	}

	errCh, doneCh := make(chan error), make(chan struct{})
	go func() {
		for _, pendingArgo := range pendingArgos {
			go func(argo v1alpha1.Workflow) {
				if err := e.syncExperimentStatusByWorkflow(argo); err != nil {
					errCh <- err
				}
			}(*pendingArgo)
		}
	}()

	go func() {
		for _, finishArgo := range finishArgos {
			go func(argo v1alpha1.Workflow) {
				if err := e.syncExperimentStatusByWorkflow(argo); err != nil {
					errCh <- err
				}
				if err := argoWorkFlowCtl.Delete(argo.Name); err != nil {
					errCh <- err
				}
			}(*finishArgo)
		}
	}()

	go func() {
		for range pendingArgos {
			<-doneCh
		}
		for range finishArgos {
			<-doneCh
		}
		close(errCh)
	}()

	for err := range errCh {
		log.Error(err)
	}

	close(doneCh)
}

func (e *ExperimentRoutine) DeleteExecutedInstanceCR() {
	clusterService := cluster.ClusterService{}
	_, restConfig, err := clusterService.GetRestConfig(context.Background(), config.DefaultRunOptIns.RunMode.Int())
	if err != nil {
		log.Error(err)
		return
	}
	log.Info("expired workflows have been deleted successfully.")

	ctx := context.Background()
	chaosmetaService := NewChaosmetaService(restConfig)
	if err := chaosmetaService.DeleteExpiredList(ctx, config.DefaultRunOptIns.WorkflowNamespace); err != nil {
		log.Error(err)
	}
	log.Info("expired chaosmeta fault experiment have been deleted successfully.")
	chaosmetaFlowInjectService := NewChaosmetaFlowService(restConfig)
	if err := chaosmetaFlowInjectService.DeleteExpiredList(ctx, config.DefaultRunOptIns.WorkflowNamespace); err != nil {
		log.Error(err)
	}
	log.Info("expired chaosmeta flow experiment have been deleted successfully.")
	chaosmetaMeasureService := NewChaosmetaMeasureService(restConfig)
	if err := chaosmetaMeasureService.DeleteExpiredList(ctx, config.DefaultRunOptIns.WorkflowNamespace); err != nil {
		log.Error(err)
	}
	log.Info("expired chaosmeta measure experiment have been deleted successfully.")
}

// ReconcileRunningInstances is the v5 DB-orchestration ticker. It scans every Running experiment
// instance and drives its nodes forward one tick via ReconcileInstance. This is what makes a running
// node's chaosmeta CR eventually flip to succeeded/failed without an Argo controller — StartExperiment
// only starts the first node synchronously; this ticker advances the rest (and picks up instances that
// survived a platform restart).
//
// Best-effort: a per-instance reconcile error is logged and skipped so one bad instance cannot stall
// the whole ticker (extreme-case graceful degradation — see task extreme-case handling requirement).
func (e *ExperimentRoutine) ReconcileRunningInstances() {
	_, instances, err := experimentInstanceModel.ListExperimentsInstancesByStatus([]experimentInstanceModel.ExperimentInstanceStatus{experimentInstanceModel.Running, experimentInstanceModel.Pending})
	if err != nil {
		log.Error("ReconcileRunningInstances: list instances error:", err)
		return
	}
	restConfig, err := getRunRestConfig()
	if err != nil {
		log.Error("ReconcileRunningInstances: get restConfig error:", err)
		return
	}
	rec := NewDBReconciler(restConfig, config.DefaultRunOptIns.WorkflowNamespace)
	for _, inst := range instances {
		status, rerr := rec.ReconcileInstance(inst.UUID)
		if rerr != nil {
			log.Error("reconcile instance", inst.UUID, "error:", rerr)
			continue
		}
		// Only persist a transition; leaving it Pending/Running unchanged avoids needless writes on the
		// hot 3s ticker. A dirty write would just no-op at the DB.
		if status != string(inst.Status) {
			if err := experimentInstanceModel.UpdateExperimentInstanceStatus(inst.UUID, status, ""); err != nil {
				log.Error("update instance", inst.UUID, "status error:", err)
			}
		}
	}
}

func (e *ExperimentRoutine) Start() {
	localCron := cron.New()
	spec := "@every 3s"

	if err := localCron.AddFunc(spec, e.DealOnceExperiment); err != nil {
		log.Error(err)
		return
	}
	if err := localCron.AddFunc(spec, e.DealCronExperiment); err != nil {
		log.Error(err)
		return
	}

	// v5 DB orchestration: drive Running/Pending instances forward every tick (replaces Argo's
	// SyncExperimentsStatus as the live-status driver for the DB-orchestrated path).
	if err := localCron.AddFunc(spec, e.ReconcileRunningInstances); err != nil {
		log.Error(err)
		return
	}

	if err := localCron.AddFunc(spec, e.SyncExperimentsStatus); err != nil {
		log.Error(err)
		return
	}

	if err := localCron.AddFunc("@every 6h", e.DeleteExecutedInstanceCR); err != nil {
		log.Error(err)
		return
	}

	localCron.Start()
	e.localCron = localCron

	select {
	case <-e.context.Done():
		log.Info("Receive stop signal")
	}
}
