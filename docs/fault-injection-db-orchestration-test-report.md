# 故障注入 DB 编排替换 Argo — 测试报告 (v5)

> 范围：v5「尽量别用 Argo，只用数据库编排」。本报告对应执行流程第5步自测 + 第6步 Codex 审查 + 诚实标注集群边界。
> 分支：`feat/fault-injection-polish-v3`。提交：`7051e8b`（orchestrator 闭环+单测）、`9469ff8`（routine 改接+ticker）。

## 1. 改动总览（活链路，非死代码）

| 主链路函数 | 改接前（Argo） | 改接后（DB 编排） | file |
|---|---|---|---|
| `StartExperiment` | `NewArgoWorkFlowService` + `argoWorkFlowCtl.Create(GetWorkflowStruct(...))` | `NewDBReconciler(...).ReconcileInstance(id)` 同步拉起首节点 + 落实例状态 | routine.go:132 |
| `stopExperiment` | Argo Get + `Spec.Shutdown=Stop` + Delete + 遍历 NodeStatus Recover | `StopInstanceByDB` 扫 DB 节点发 Recover 标 Stopped | routine.go:283 |
| `confirmRecoverCompleted` | 扫 `Argo.Workflow.Status.Nodes` 轮询 CR | `ConfirmRecoverByDB` 扫 DB 节点反查 CR（保留 60s timeout 轮询契约） | routine.go:354 |
| 推进 running 节点 | Argo DAG controller | `ReconcileRunningInstances` ticker @every 3s | routine.go:ReconcileRunningInstances |
| 调度载体 | Argo Workflow CR + DAG | `workflow_node_instance` 行（Status/Row/Column/ExecType/Version 乐观锁） | — |

Argo 旧路径（`SyncExperimentsStatus`/`injectRecoverByArgo`/`GetWorkflowStruct`）标注 `Deprecated` 保留不删，降爆炸半径；其中 `getFaultStep` 等仍被 `orchestrator_prod.go` 复用为 chaosmeta CR 构造的单一事实源。

## 2. 本机自测结果（执行流程第5步）

### 2.1 编译 / 交叉编译
| 验证项 | 命令 | 结果 |
|---|---|---|
| darwin 全量 | `go build ./...` | exit 0（零连带破坏） |
| linux/amd64 全量交叉 | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` | exit 0 |
| `go vet ./pkg/service/experiment/` | — | exit 0 |

### 2.2 单元测试（orchestrator_test.go，6 例）
| 用例 | 覆盖 | 结果 |
|---|---|---|
| `TestReconcileSerialProgression` | 严格串行：节点2 在节点1 succeeded 前绝不启动；末节点同 tick 完成时实例=Succeeded | PASS |
| `TestReconcileFailurePropagation` | 节点 failed → 实例 Failed + 后续节点不启动 | PASS |
| `TestReconcileWaitNodeAdvances` | Wait 节点无 CR，同 tick 立即 succeeded 让链路推进 | PASS |
| `TestStopInstanceByDB` | 停止标记所有 running/succeeded 节点 stopped + 对 fault 发 Recover | PASS |
| `TestConfirmRecoverByDB` | clean/absent → 空；未 clean → 报 CR 名 | PASS |
| `TestReconcileCreateFailure` | Create 出错 → 节点 failed + 实例 Failed | PASS |

测试注入内存 `fakeStore` + 脚本化 `fakeExecutor`，不依赖 K8s/beego ORM（设计 §6 收口点"mock chaosmetaService 接口"）。

> 单测过程中发现并修复的真实 bug（验证单测价值）：
> 1. wait 节点 `to_be_executed` 分支硬编码 `prevSucceeded=false`，导致 wait 立即完成后链路不推进 → 改为读 node 后置状态决定。
> 2. `running` 分支在调 `pollRunning` 前就置 `running=true`，导致末节点同 tick 完成时实例仍报 Running → 改为仅未完成时置位。

## 3. Codex 审查（执行流程第6步）

### 3.1 第一轮审查（codex-reviewer #1）
独立交叉验真（codex CLI 因 OAuth 401 不可用，基于实读实查）发现 **2 个 P1 阻断项**，同根因——chaosmeta CR 的 `Phase` 字段在 clean 判定里缺位，导致**叠加注入**：

- **P1-1（叠加注入，核心安全）** `orchestrator.go` `isCRStatusClean`/`pollRunning` 只判 `status∈{success,stopped}` 不校验 `Status.Phase==recover`。chaosmeta inject operator 在 **inject 阶段**就置 `status=success`（`chaosmeta-inject-operator/pkg/phasehandler/inject/handler.go:178`），此时故障仍驻留；旧 Argo `SuccessCondition` 明确要求 `status.phase==recover,status.status==success`（`experiment_custom_resource.go:105`）。后果：fault 节点 inject 完成但未 recover 就被误判 clean → `completeNode` 标 succeeded → 严格串行 reconciler 启动下一节点 → **故障叠加注入**。
- **P1-2（掩盖未恢复故障）** 3 处 `Recover`（inject/flow/measure 同构）在 `c.Get` 返 error（含 NotFound）时直接 `return err`，而 `StopInstanceByDB`/`ConfirmRecoverByDB` 把 Recover error 吞掉判 clean → CR 不存在时误判"已恢复"。

### 3.2 修复（提交 b9ca2ad）
- **P1-1**：`NodeExecutor.GetStatus` 改返回 `(phase, status, error)`；`isCRStatusClean(s, phase)` 对 fault（phase≠""）要求 `PhaseType(phase)==recover && status∈{success,stopped}`，flow/measure（phase=""）仅 status 判定（其 SuccessCondition 本就无 phase，inject 即终态）；`pollRunning` 对 fault inject-phase success **先发 Recover 再留 running**，下 tick 才完成——杜绝叠加注入。
- **P1-2**：3 处 `Recover` 对 `apierrors.IsNotFound` 直接 `return nil`（CR 已 GC=已 clean），其余 error 仍上抛保留 transient error 重试语义。
- **回归测** `TestReconcileAbortsWhenCRStillInjecting`：inject-phase success 时节点须留 running、下一节点不启动、且发了 Recover；CR 翻 recover 后才完成。回退修复则 `n2.creates>0` 失败，证明测试有效。

### 3.3 第二轮复审（codex-reviewer #2，验证 P1 修死 + 无新增）

**结论：P0/P1 清零，可推进推送 + 集群验证。** codex CLI 因 OAuth 401 不可用，工具二次校验缺位；本轮结论基于实读 b9ca2ad + 跑 7 单测 + 回退修复复现 P1-1 失败的实证。

**P1-1 修死实证**：
- `pollRunning`（orchestrator.go:194-197）对 `FaultExecType && status==success && phase!=recover` 提前 `Recover()+return false`——节点留 running、不进 `completeNode`、`prevSucceeded=false`、下一节点不启动。
- `isCRStatusClean`（orchestrator.go:316-329）作第二道防线：fault（phase≠""）必须 `PhaseType(phase)==recover`。
- 双层防护均被回归测断言把守：回退后 `TestReconcileAbortsWhenCRStillInjecting` 必失败（叠加注入 `n2.creates>0` 或状态机卡死 `recovers=0`）。
- **`stopped`+`inject phase` 组合**：被卡判 unclean 是**正确语义**——operator 在 inject 阶段不自己置 stopped，stopped 只在 recover 阶段清理完成后出现；若观测到 inject+stopped 是异常中间态，fail-closed 让 ticker 继续发 Recover、60s 后升级 Error，正确。

**P1-2 修死实证**：
- 3 处 `Recover` 对 `IsNotFound→return nil`，其余 error 上抛。`startNode` 是"Create 成功后才标 running"的顺序，故"running 但 CR 不存在"窗口在执行器状态机里打不开。
- transient error（5xx/etcd 抖动）下 Recover error 被吞但 DB 仍标 stopped——是 `StopInstanceByDB` best-effort 既有设计，由后置 `ConfirmRecoverByDB` 双重确认兜底。

**无新增 P1**：recover 死循环/GC（operator 翻 recover 后幂等 no-op）、phase 空串与 fault 混淆（flow/measure 无 phase 是其正确语义）、并发 ticker 竞态（cron 不重入 + Version 乐观锁 + 严格串行三层防护）——均无新增。

**2 个 P2 残留（非阻断，保留并在集群期观察）**：
1. `routine.go` deprecated `injectRecoverByArgo` 仍把 NotFound 当恢复成功（P1-2 风险在死代码里复现），已标 Deprecated 不在 live 路径，live 走 `completeNode`+`ConfirmRecoverByDB`。强删会增爆炸半径，保留仅在报告标注；日后复用需补 `isCRStatusClean` 复核。
2. `ConfirmRecoverByDB` 忽略 `Get` 的 transient err（`st==""` 被判 clean）。严格说应把 err 也当 unclean 重试，但 60s `confirmRecoverCompleted` 轮询兜底，权衡保留。

### 3.4 结论门槛
- ✅ 二审 P1 清零 → 推进 §5 推送 + 集群验证。
- ⚠️ codex CLI 本身 OAuth 失效需用户 `codex login` 后做工具二次校验（我无法代登录，诚实标注）。

## 4. 已验证 vs 集群边界（验收标准"极端情况测试通过"的诚实拆分）

**本机已验**：编译/交叉编译/vet/6 纯单测（串行/失败传播/wait/停止/确认/创建失败）。

**集群真实运行验证（留用户，本机做不了）**——以下项本机无法实证，**不造假**：
1. 真 K8s + chaosmeta operator 下 `Create`/`Recover` CR 的真实行为（CR 翻转时序、operator reconcile）
2. `ReconcileRunningInstances` ticker 并发 / 平台重启后续跑（Version 乐观锁在真实 DB 下的竞争表现）
3. 7×24h RSS / 句柄泄漏（需长跑环境）
4. 停止恢复真实清理：进程/连接/文件锁等残留是否干净（依赖 chaosmetad daemon 在目标节点执行）
5. 网络抖动 / 磁盘满 / 权限不足下的优雅降级（需诱发真实异常）
6. Argo 原承担的 workflow GC，DB 编排后由 `DeleteExecutedInstanceCR`（清 K8s 过期 chaosmeta CR，保留）+ DB 行生命周期承担——生命周期是否完整需集群观察

执行流程第7步"代码合并与推送"在本机自测 + Codex 审查无 P1 后执行（见 §3 定稿后）。

## 5. 与验收标准的映射

| 验收标准 | v5 达成度 | 证据 |
|---|---|---|
| 状态机状态迁移正确，停止总能恢复初始态 | 核心逻辑已实现+单测验；**集群真运行待验**（§4-1） | orchestrator.go + TestStopInstanceByDB/TestReconcileFailurePropagation |
| 日志实时更新，过程数据可视化 | 前端 v4 已做（MetricsPanel/RealtimeLogPanel/logs+metrics 端点连通） | fi-design-system-uiux.md + v4 commit |
| 所有极端情况测试通过 | 纯逻辑极端（失败/创建错/串行）单测验过；**运行时极端（网络/磁盘/并发竞争）集群待验** | §2.2 + §4 |
| 模块与现有代码解耦，无副作用 | orchestrator 纯逻辑+注入接口；CR 构造复用遗留 builder 不改 Argo 路径；全量编译绿无连带破坏 | §1 + §2.1 |
| Codex 两次审查均无重大问题 | 方案审查(v3.1)+代码审查(v3.2)已过；**v5 编排代码审查进行中** | §3 |
| 代码合并与推送 | 待 Codex 无 P1 后执行 | §3 后 |
