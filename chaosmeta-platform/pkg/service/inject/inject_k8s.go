package inject

import (
	"chaosmeta-platform/pkg/models/inject/basic"
	"context"
)

// k8S-Target
func InitK8STarget(ctx context.Context, scope basic.Scope) error {
	var (
		K8SPodTarget        = basic.Target{Name: "pod", NameCn: "pod", Description: "fault injection capabilities related to cloud-native resource pod instances", DescriptionCn: "云原生资源 pod 实例相关的故障注入能力"}
		K8SDeploymentTarget = basic.Target{Name: "deployment", NameCn: "deployment", Description: "fault injection capabilities related to cloud-native resource deployment instances", DescriptionCn: "云原生资源 deployment 实例相关的故障注入能力"}
		K8SNodeTarget       = basic.Target{Name: "node", NameCn: "node", Description: "fault injection capabilities related to cloud-native resource node instances", DescriptionCn: "云原生资源 node 实例相关的故障注入能力"}
		K8SClusterTarget    = basic.Target{Name: "cluster", NameCn: "cluster", Description: "fault injection capabilities related to kubernetes macro cluster risks", DescriptionCn: "kubernetes 宏观的集群性风险相关的故障注入能力"}
	)
	K8SPodTarget.ScopeId = scope.ID
	if err := basic.InsertTarget(ctx, &K8SPodTarget); err != nil {
		return err
	}
	if err := InitPodFault(ctx, K8SPodTarget); err != nil {
		return err
	}
	K8SDeploymentTarget.ScopeId = scope.ID
	if err := basic.InsertTarget(ctx, &K8SDeploymentTarget); err != nil {
		return err
	}
	if err := InitDeploymentFault(ctx, K8SDeploymentTarget); err != nil {
		return err
	}
	K8SNodeTarget.ScopeId = scope.ID
	if err := basic.InsertTarget(ctx, &K8SNodeTarget); err != nil {
		return err
	}
	if err := InitNodeFault(ctx, K8SNodeTarget); err != nil {
		return err
	}
	K8SClusterTarget.ScopeId = scope.ID
	if err := basic.InsertTarget(ctx, &K8SClusterTarget); err != nil {
		return err
	}
	return InitClusterFault(ctx, K8SClusterTarget)
}

// pod
func InitPodFault(ctx context.Context, podTarget basic.Target) error {
	var (
		podFaultDelete         = basic.Fault{TargetId: podTarget.ID, Name: "delete", NameCn: "删除Pod", Description: "delete the target Pod instance", DescriptionCn: "删除目标Pod实例"}
		podFaultLabel          = basic.Fault{TargetId: podTarget.ID, Name: "label", NameCn: "修改Pod标签", Description: "modify the label of the target Pod instance", DescriptionCn: "修改目标Pod实例的标签"}
		podFaultFinalizer      = basic.Fault{TargetId: podTarget.ID, Name: "finalizer", NameCn: "增加Pod finalizer", Description: "add a finalizer to the target Pod instance", DescriptionCn: "为目标Pod实例增加finalizer"}
		podFaultContainerKill  = basic.Fault{TargetId: podTarget.ID, Name: "containerkill", NameCn: "杀掉Pod中的容器", Description: "kill the specified container in the target Pod instance", DescriptionCn: "杀掉目标Pod实例中指定的容器"}
		podFaultContainerPause = basic.Fault{TargetId: podTarget.ID, Name: "containerpause", NameCn: "暂停Pod中的容器", Description: "pauses the specified container in the target Pod instance", DescriptionCn: "暂停目标Pod实例中指定的容器"}
		podFaultContainerImage = basic.Fault{TargetId: podTarget.ID, Name: "containerimage", NameCn: "修改Pod容器镜像", Description: "modify the image of the specified container in the target Pod instance", DescriptionCn: "修改目标Pod实例中指定容器的镜像"}
	)
	if err := basic.InsertFault(ctx, &podFaultDelete); err != nil {
		return err
	}

	if err := basic.InsertFault(ctx, &podFaultLabel); err != nil {
		return err
	}
	if err := InitPodTargetArgsLabel(ctx, podFaultLabel); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &podFaultFinalizer); err != nil {
		return err
	}
	if err := InitPodTargetArgsFinalizer(ctx, podFaultFinalizer); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &podFaultContainerKill); err != nil {
		return err
	}
	if err := InitPodTargetArgsContainerKillAndPause(ctx, podFaultContainerKill); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &podFaultContainerPause); err != nil {
		return err
	}
	if err := InitPodTargetArgsContainerKillAndPause(ctx, podFaultContainerPause); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &podFaultContainerImage); err != nil {
		return err
	}
	return InitPodTargetArgsContainerImage(ctx, podFaultContainerImage)
}

func InitPodTargetArgsLabel(ctx context.Context, podFault basic.Fault) error {
	argsAdd := basic.Args{InjectId: podFault.ID, ExecType: ExecInject, Key: "add", KeyCn: "增加的标签", UnitCn: "逗号分隔的键值对列表", Unit: "comma-separated key-value pair list", ValueType: "string", DefaultValue: "", Description: "added labels", DescriptionCn: "增加的标签"}
	argsDelete := basic.Args{InjectId: podFault.ID, ExecType: ExecInject, Key: "delete", KeyCn: "删除的标签key", UnitCn: "逗号分隔的字符串列表", Unit: "comma-separated string list", ValueType: "string", DefaultValue: "", Description: "deleted lebel key", DescriptionCn: "删除的标签"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsAdd, &argsDelete})
}

func InitPodTargetArgsFinalizer(ctx context.Context, podFault basic.Fault) error {
	argsAdd := basic.Args{InjectId: podFault.ID, ExecType: ExecInject, Key: "add", KeyCn: "增加的finalizer", UnitCn: "逗号分隔的finalizer名称列表", Unit: "comma-separated key-value pair list", ValueType: "string", DefaultValue: "", Description: "added finalizers", DescriptionCn: "增加的finalizer"}
	argsDelete := basic.Args{InjectId: podFault.ID, ExecType: ExecInject, Key: "delete", KeyCn: "删除的标签finalizer", UnitCn: "逗号分隔的finalizer名称列表", Unit: "comma-separated string list", ValueType: "string", DefaultValue: "", Description: "deleted finalizer key", DescriptionCn: "删除的finalizer"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsAdd, &argsDelete})
}

func InitPodTargetArgsContainerKillAndPause(ctx context.Context, podFault basic.Fault) error {
	argsContainerName := basic.Args{InjectId: podFault.ID, ExecType: ExecInject, Key: "containername", KeyCn: "目标容器名称", UnitCn: "具体的容器名称，或者“firstcontainer”，表示pod中第一个容器", Unit: "Specific container name, or 'firstcontainer' which represents the first container in the pod", ValueType: "string", DefaultValue: "", Description: "target container name", DescriptionCn: "目标容器名称"}
	return basic.InsertArgs(ctx, &argsContainerName)
}

func InitPodTargetArgsContainerImage(ctx context.Context, podFault basic.Fault) error {
	argsContainerName := basic.Args{InjectId: podFault.ID, ExecType: ExecInject, Key: "containername", KeyCn: "目标容器名称", UnitCn: "具体的容器名称，或者“firstcontainer”，表示pod中第一个容器", Unit: "Specific container name, or 'firstcontainer' which represents the first container in the pod", ValueType: "string", DefaultValue: "", Description: "target container name", DescriptionCn: "目标容器名称"}
	argsImage := basic.Args{InjectId: podFault.ID, ExecType: ExecInject, Key: "image", KeyCn: "镜像名称", UnitCn: "目标镜像名称", Unit: "Target image name", ValueType: "string", DefaultValue: "", Description: "Target image name", DescriptionCn: "目标镜像名称"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsContainerName, &argsImage})
}

// deployment
func InitDeploymentFault(ctx context.Context, deploymentTarget basic.Target) error {
	var (
		deploymentFaultDelete    = basic.Fault{TargetId: deploymentTarget.ID, Name: "delete", NameCn: "删除deployment", Description: "delete target deployment", DescriptionCn: "删除目标deployment"}
		deploymentFaultLabel     = basic.Fault{TargetId: deploymentTarget.ID, Name: "label", NameCn: "修改deployment标签", Description: "modify the label of the target deployment", DescriptionCn: "修改目标deployment的标签"}
		deploymentFaultFinalizer = basic.Fault{TargetId: deploymentTarget.ID, Name: "finalizer", NameCn: "增加deployment finalizer", Description: "add a finalizer to the target deployment", DescriptionCn: "为目标deployment增加finalizer"}
		deploymentFaultReplicas  = basic.Fault{TargetId: deploymentTarget.ID, Name: "replicas", NameCn: "篡改deployment副本数", Description: "tampering with the number of copies of the target deployment", DescriptionCn: "篡改目标deployment的副本数"}
	)
	if err := basic.InsertFault(ctx, &deploymentFaultDelete); err != nil {
		return err
	}
	if err := InitDeploymentDeleteArgs(ctx, deploymentFaultDelete); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &deploymentFaultLabel); err != nil {
		return err
	}
	if err := InitDeploymentLabelArgs(ctx, deploymentFaultLabel); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &deploymentFaultFinalizer); err != nil {
		return err
	}
	if err := InitDeploymentFinalizerArgs(ctx, deploymentFaultFinalizer); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &deploymentFaultReplicas); err != nil {
		return err
	}
	return InitDeploymentReplicasArgs(ctx, deploymentFaultReplicas)
}

func InitDeploymentDeleteArgs(ctx context.Context, deploymentFault basic.Fault) error {
	return nil
}

func InitDeploymentLabelArgs(ctx context.Context, deploymentFault basic.Fault) error {
	argsAdd := basic.Args{InjectId: deploymentFault.ID, ExecType: ExecInject, Key: "add", KeyCn: "增加的标签", Unit: "A comma-separated list of key-value pairs in the format: k1=v1,k2=v2", UnitCn: "逗号分隔的键值对列表，格式为：k1=v1,k2=v2", ValueType: "string", DefaultValue: "", Description: "added labels", DescriptionCn: "增加的标签"}
	argsDelete := basic.Args{InjectId: deploymentFault.ID, ExecType: ExecInject, Key: "delete", KeyCn: "删除的标签", Unit: "a comma-separated list of strings in the format: k1,k2", UnitCn: "逗号分隔的字符串列表，格式为：k1,k2", ValueType: "string", DefaultValue: "", Description: "deleted label", DescriptionCn: "删除的标签"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsAdd, &argsDelete})
}

func InitDeploymentFinalizerArgs(ctx context.Context, deploymentFault basic.Fault) error {
	argsAdd := basic.Args{InjectId: deploymentFault.ID, ExecType: ExecInject, Key: "add", KeyCn: "增加finalizer", Unit: "A comma-separated list of finalizer names in the format: c/1,c/2", UnitCn: "逗号分隔的finalizer名称列表，格式为：c/1,c/2", ValueType: "string", DefaultValue: "", Description: "added finalizer", DescriptionCn: "增加的finalizer"}
	argsDelete := basic.Args{InjectId: deploymentFault.ID, ExecType: ExecInject, Key: "delete", KeyCn: "删除finalizer", Unit: "逗号分隔的finalizer名称列表，格式为：c/1,c/2", UnitCn: "逗号分隔的finalizer名称列表，格式为：c/1,c/2", ValueType: "string", DefaultValue: "", Description: "removed finalizers", DescriptionCn: "删除的finalizer"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsAdd, &argsDelete})
}

func InitDeploymentReplicasArgs(ctx context.Context, deploymentFault basic.Fault) error {
	argsMode := basic.Args{InjectId: deploymentFault.ID, ExecType: ExecInject, Key: "mode", KeyCn: "模式", Unit: "absolutecount、relativecount、relativepercent", UnitCn: "absolutecount、relativecount、relativepercent", ValueType: "string", DefaultValue: "", Description: "scaling mode", DescriptionCn: "扩缩容模式"}
	argsValue := basic.Args{InjectId: deploymentFault.ID, ExecType: ExecInject, Key: "value", KeyCn: "扩所容值", UnitCn: "数值，在三种模式下表示不同含义。absolutecount：最终目标副本数；relativecount：相对旧副本数增加或减少了多少个；relativepercent：相对旧副本数增加或减少了百分之多少", Unit: "Numerical values, with different meanings in the three modes\nabsolutecount: the final target number of copies\nrelativecount: how much has been increased or decreased relative to the number of old copies\nrelativepercent: the percentage increase or decrease relative to the number of old copies", ValueType: "string", DefaultValue: "", Description: "scale size", DescriptionCn: "扩缩容大小"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsMode, &argsValue})
}

// node
func InitNodeFault(ctx context.Context, nodeTarget basic.Target) error {
	var (
		nodeFaultLabel = basic.Fault{TargetId: nodeTarget.ID, Name: "label", NameCn: "修改node标签", Description: "modify the label of the target node", DescriptionCn: "修改目标node的标签"}
		nodeFaultTaint = basic.Fault{TargetId: nodeTarget.ID, Name: "taint", NameCn: "增加node taint", Description: "add taint to the target node", DescriptionCn: "为目标node增加taint"}
	)
	if err := basic.InsertFault(ctx, &nodeFaultLabel); err != nil {
		return err
	}
	if err := InitNodeLabelArgs(ctx, nodeFaultLabel); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &nodeFaultTaint); err != nil {
		return err
	}
	return InitNodeTaintArgs(ctx, nodeFaultTaint)
}

func InitNodeLabelArgs(ctx context.Context, nodeFault basic.Fault) error {
	return InitDeploymentLabelArgs(ctx, nodeFault)
}

func InitNodeTaintArgs(ctx context.Context, nodeFault basic.Fault) error {
	argsAdd := basic.Args{InjectId: nodeFault.ID, ExecType: ExecInject, Key: "add", KeyCn: "增加的taint", Unit: "a comma-separated list of taints in the format: k1=v1:NoSchedule,k2=v2:NoSchedule", UnitCn: "逗号分隔的taint列表，格式为：k1=v1:NoSchedule,k2=v2:NoSchedule", ValueType: "string", DefaultValue: "", Description: "increased taint", DescriptionCn: "增加的taint"}
	argsDelete := basic.Args{InjectId: nodeFault.ID, ExecType: ExecInject, Key: "delete", KeyCn: "删除的taint", Unit: "A comma-separated list of taints in the format: k1=v1:NoSchedule,k2=v2:NoSchedule", UnitCn: "逗号分隔的taint列表，格式为：k1=v1:NoSchedule,k2=v2:NoSchedule", ValueType: "string", DefaultValue: "", Description: "removed taint", DescriptionCn: "删除的taint"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsAdd, &argsDelete})
}

//cluster

func InitClusterFault(ctx context.Context, clusterTarget basic.Target) error {
	var (
		clusterFaultPendingPod   = basic.Fault{TargetId: clusterTarget.ID, Name: "pendingpod", NameCn: "堆积pending状态的pod", Description: "accumulate a large number of pods in the pending state for the cluster in batches\"", DescriptionCn: "给集群批量堆积大量pending状态的pod"}
		clusterFaultCompletedJob = basic.Fault{TargetId: clusterTarget.ID, Name: "completedjob", NameCn: "堆积completed状态的job", Description: "accumulate a large number of jobs in the completed state in batches for the cluster", DescriptionCn: "给集群批量堆积大量completed状态的job"}
	)
	if err := basic.InsertFault(ctx, &clusterFaultPendingPod); err != nil {
		return err
	}
	if err := InitClusterPendingPodArgs(ctx, clusterFaultPendingPod); err != nil {
		return err
	}
	if err := basic.InsertFault(ctx, &clusterFaultCompletedJob); err != nil {
		return err
	}
	return InitClusterCompletedJobArgs(ctx, clusterFaultCompletedJob)
}

func InitClusterPendingPodArgs(ctx context.Context, clusterFault basic.Fault) error {
	return InitClusterCompletedJobArgs(ctx, clusterFault)
}

func InitClusterCompletedJobArgs(ctx context.Context, clusterFault basic.Fault) error {
	argsCount := basic.Args{InjectId: clusterFault.ID, ExecType: ExecInject, Key: "count", KeyCn: "数量", Unit: "number greater than 0", UnitCn: "大于0的数字", ValueType: "int", DefaultValue: "", Description: "count", DescriptionCn: "数量"}
	argsNamespace := basic.Args{InjectId: clusterFault.ID, ExecType: ExecInject, Key: "namespace", KeyCn: "命名空间", Unit: "a namespace that does not exist in the current cluster, for example: \"pendingattack\"", UnitCn: "当前集群不存在的namespace，比如：“pendingattack”", ValueType: "string", DefaultValue: "", Description: "namespace", DescriptionCn: "命名空间"}
	argsName := basic.Args{InjectId: clusterFault.ID, ExecType: ExecInject, Key: "name", KeyCn: "pod名称前缀", Unit: "the name prefix of the injected completed job, for example: \"attack-test”", UnitCn: "注入的completed job的名称前缀，比如：“attack-test”", ValueType: "string", DefaultValue: "", Description: "pod name prefix", DescriptionCn: "pod名称前缀"}
	return basic.InsertArgsMulti(ctx, []*basic.Args{&argsCount, &argsNamespace, &argsName})
}