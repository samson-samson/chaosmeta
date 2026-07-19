# 故障注入：DB 编排替换 Argo Workflow（设计方案）

> 目标：故障注入实验执行**去掉 Argo Workflow 依赖，改用 DB 状态机 + platform reconcile 循环编排**。唯一的外部执行依赖收敛到 chaosmeta operator（Experiment/LoadTest/Measure CR）。本机只交叉编译+纯单测验；K8s+operator 集群真验证留用户。

## 0. 现状：Argo 在链路里的真实职责（已读实码确认）

| Argo 职责 | 代码点 | DB 编排如何替换 |
|---|---|---|
| 编排骨架（DAG） | `GetWorkflowStruct`/`convertToSteps` 生成 `v1alpha1.Workflow` CR | **不创 Argo CR**；拓扑（Row/Column 串行链）直接由 reconcile 按 `OrderBy("row","column")` 驱动 `workflow_node_instance` 行 |
| 节点执行（create chaosmeta CR） | Argo `ResourceTemplate` action=create，manifest=节点 CR yaml | platform reconcile **直接调** `chaosmetaService.Create(ctx, cr)`（inject/flow/measure 各自 service 已有 Create） |
| 节点成功判定 | Argo `SuccessCondition: status.phase==recover,status.status==success` | reconcile 轮询 `chaosmetaService.Get(ctx, ns, name).Status`，`isCleanTerminal()` 判定（已有） |
| 失败传播 | Argo `FailFast=true` | reconcile 遇节点 failed/error → 整实例标 Failed，停止推进后续节点 |
| 启动 | `StartExperiment`→`argoWorkFlowCtl.Create` | `StartExperiment`→创实例行(Pending)→**reconcile 立刻开始拉起首个节点**（不再创 Argo） |
| 停止 | `stopExperiment` Patch Workflow.Shutdown=Stop + Delete + 遍历节点 Recover | `stopExperiment` 遍历**DB 节点**（非 Argo NodeStatus）→对每个已注入节点 `Recover` + 标 Stopped；删无外部 CR 要清 |
| 恢复确认 | `confirmRecoverCompleted` 扫 Argo Workflow.Status.Nodes | 扫 **DB 节点**对应的 chaosmeta CR（CR name 仍由 `getInjectStepName` 等算出），轮询到 clean terminal |

**关键简化**：topology 实为**严格串行**（行间串行+行内串行，FailFast）。无需复杂 DAG 引擎，一个按 Row/Column 顺序推进的状态机即可。

## 1. 状态机（DB 载体：`workflow_node_instance.Status`，已有字段）

复用现有 `to_be_executed`(默认) → `running` → `succeeded`/`failed`/`error`/`stopped`。新增派生态供 reconcile 用（沿用 Argo 期语义）：
- `to_be_executed`：未调度
- `running`：已创 chaosmeta CR，等成功
- `succeeded`：CR 到 success（inject 阶段）/ recover success（recover 阶段）
- `failed`：CR failed，实例整体 Failed
- `stopped`：stop 被触发，已发 Recover
- `error`：异常（发散保留）

实例级 `experiment_instance.Status`：`Pending`→`Running`→`Succeeded`/`Failed`/`Error`（沿用 WorkflowSucceeded 等常量名，避免前端/常量重命名扩散）。

## 2. reconcile 循环（新增 `orchestrator.go`）

```
ReconcileInstance(instanceUUID):
  nodes := GetWorkflowNodeInstancesByExperimentUUID(instanceUUID)  // 已 OrderBy(row,column)
  if instance done: return
  for i, node := range nodes:
    switch node.Status:
      to_be_executed:
        if i==0 || prev(node).succeeded:
           startNode(node)  // 创 chaosmeta CR + 标 running + persistLog
        else: break  // 串行：前一个没成功不启
      running:
        cr := chaosmetaService.Get(...)
        if cr==nil: // CR 没了
           markNode(node, succeeded/failed by 语义)
        elif isCleanTerminal(cr.Status.Status):
           markNode(node, succeeded)
           // fault 节点：成功后要发 recover(原 Argo success-condition 含 phase==recover)
           if node.ExecType==Fault: chaosmetaService.Recover(...)
        elif cr failed: markNode(node, failed); setInstance(Failed); return
      succeeded: continue
      failed/error/stopped: setInstance(derive); return
  if all succeeded: setInstance(Succeeded)
```

驱动触发：两个来源①前端 start/stop 调用后**同步跑一次** `ReconcileInstance`（即时拉起）②后台 ticker（`DealOnceExperiment` 同款定时器，~2s 扫 Running 态实例推进 running 节点）。**不依赖 Argo informer**。

## 3. startNode：直接创 CR（不经 Argo ResourceTemplate）

从 `getFaultStep`/`getFlowStep`/`getMeasureStep` **抽出 CR 构造逻辑**为纯函数 `buildFaultCR(node, phaseType) *ExperimentInjectStruct` 等（去掉 Argo `DAGTask`/`Arguments` 外壳，保留 CR 本体 + step name 计算）。
- WaitExecType 节点：DB 里只记 `running`，reconcile 用 `time.Sleep(duration)` 或定时到点标 succeeded（不创 CR）。
- 创建：`chaosmetaService.Create(ctx, cr)`。**CR name = 现有 `getInjectStepName(...)` 同算法**（保证 stop/confirm 能反查到同名 CR）。

## 4. stop：纯 DB + 直发 Recover

`stopExperiment` 改为：
```
nodes := GetWorkflowNodeInstancesByExperimentUUID(id)
for node in nodes where node.Status in {running, succeeded}:
   crName := stepName(node)  // 同 buildCR 用的 name
   chaosmetaService.Recover(ns, crName)  // 容错：CR 不存在=已清
   markNode(node, stopped)
setInstance(Stopped/Succeeded by confirm)
```
`confirmRecoverCompleted` 改为扫 **DB 节点**反查 CR（不依赖 Argo Workflow.Status.Nodes）。

## 5. 爆炸半径 / 兼容

- **保留** `argo_workflow.go`/`experiment_custom_resource.go` 文件不删（避免大删动牵连），但 routine 不再调 `NewArgoWorkFlowService`。可加 `// Deprecated: DB orchestration replaced Argo, kept for reference` 标注。
- 客户端 import `"argoproj/argo-workflows/v3/.../v1alpha1"` 仍在 `experiment_custom_resource.go`（因 buildCR 抽出后该文件只剩 Argo 外壳，理论上可后续清理，但本轮保守保留以降风险）。
- 前端**零改动**：start/stop/logs/metrics 端点不变（platform 内部执行器换了，HTTP 契约不变）。
- chaosmetad/operator **零改动**：CR 契约不变（platform 直接创同名 CR，operator reconcile 不变）。

## 6. 本机可验证 vs 集群边界

**本机可验（本次做）**：
- platform GOOS=linux 交叉编译绿（去掉 Argo 调用后 client-go/argo 依赖若仍 import 即仍编进；若要真正减依赖需删 import，本轮评估后定）
- 单测：`orchestrator` reconcile 纯逻辑（mock chaosmetaService 接口，验证串行推进/失败传播/stop 恢复）——抽 interface 让 K8s client 可 mock
- alpine 沙箱跑单测

**集群边界（留用户，诚实标）**：
- 真 K8s+operator 下 Create/Recover CR 的真实行为
- reconcile ticker 并发/重启续跑
- 7×24h RSS、Argo 原先承担的 workflow GC

## 7. 实施步骤

1. 抽 `buildFaultCR/buildFlowCR/buildMeasureCR` 纯函数（从 getFaultStep 等剥离 Argo 外壳）
2. 定义 `chaosmetaExecutor` interface（Get/Create/Recover），让现有 3 个 service 已有方法满足
3. 写 `orchestrator.go`：`ReconcileInstance` + `startNode` + `stopInstance`（DB 驱动）
4. 改 `routine.go`：`StartExperiment` 不创 Argo（创实例后调 ReconcileInstance）；`stopExperiment`/`confirmRecoverCompleted` 扫 DB 节点；`injectRecoverByArgo` 弃用
5. 加 reconcile ticker（挂到现有定时任务入口）
6. 单测 + 交叉编译
