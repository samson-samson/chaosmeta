# chaosmeta 故障注入模块优化方案（任务1 + 任务2）

> 版本: v2 (2026-07-17) — 自审修订版（不走外部 codex 审查）
> v2 修订三处爆炸半径硬伤：
>   1. **D5 修复不可编译**：旧写 `return i.recoverInternal(ctx)`，但 `recoverInternal` 不存在；真实修复=基类短路删 `StatusError` 一行，子注入器委托链天然落到内联 recover（§2.2.3）。
>   2. **D6 stale-scan 误杀活实验**：注入成功写 `success`（驻留态）+ fork 脱离孤儿 timer（chaosmetad 重启后仍存活）。旧写「启动扫所有 success 去 recover」=重启即清，灾难级破坏爆炸半径。改为**只救丢 timer 且过 deadline 的真残留**，绝不扫定时器还活的（§2.3.1）。为此新增 `orphan_pid`/`recover_deadline` 可追踪列（§2.3.0），同时解决「停止/暂停」需停孤儿 timer 的问题。
>   3. **引擎状态不重命名不迁移**：废止 `success`→`running` 重命名与启动期 DB 迁移——那本身会触碰必须保持驻留的记录。引擎 `success` 保留「驻留中」语义，用户态 `Running` 由 adapter 映射（§2.1.3）。
> 目标: 在 **chaosmetad 引擎 + inject-operator + platform 后端 + platform-frontend** 四层上，完成
>   **任务1（前端页面重构）** 与 **任务2（核心故障注入逻辑增强：状态机 / 干净停止与恢复 / 日志与过程数据）**，
> 并满足非功能需求（爆炸半径、极端场景、7×24h 长跑、隔离性）。
> 约束: chaosmetad 依赖 containerd/cgroups v1.0.4 的 Linux-only syscall，**macOS 无法构建**；编译与单测须在 Linux/集群上完成。

---

## 0. 现状摘要与缺陷清单（排查结论）

### 0.1 四层架构与执行链路

```
用户层    chaosmeta-platform-frontend (umijs/max + antd5 + echarts5)
            └─ ExperimentController.ts HTTP 调用
API 层    chaosmeta-platform (Go beego)
            └─ StartExperiment / StopExperiment(UserStopExperiment)
            └─ Argo Workflow 编排（每个 inject/measure/flow step = 1 个 Argo node = 1 个 K8s CR）
编排层    chaosmeta-inject-operator
            └─ Experiment CR reconcile → daemonset-exec + nsenter 到节点 PID1 → chaosmetad CLI
引擎层    chaosmetad (Go daemon, 节点上常驻)
            └─ HTTP /v1/experiment/inject /query /recover，SQLite(gorm) 存储
            └─ 注入驻留模型: 孤儿 sleep 进程 (sleep N; chaosmetad recover <uid>) / 内核态 (tc qdisc) / 停止的进程态
            └─ chaosmetad 主进程不持有内存句柄
```

### 0.2 已确认缺陷（带 file:line 证据）

| # | 层 | 缺陷 | 证据 | 影响 |
|---|----|------|------|------|
| D1 | inject-operator | **CRD 无 paused/stopped/error 状态**，仅 `created/success/failed/running/partSuccess` | `experiment_types.go:67-73` | 无法表达"暂停/已停止/异常"；状态机不完整 |
| D2 | chaosmetad | **引擎仅 4 状态** `created/success/error/destroyed`，无 `running/recovering/paused/stopped` | `pkg/utils/common.go:62-65` | 运行中过程不可见；异常无独立态 |
| D3 | inject-operator | **删除路径未覆盖 running/created**：仅 `success/failed/partSuccess` 才走 recover | `experiment_controller.go:82-93` | 运行中删除 CR 不触发 recover → chaosmetad 残留 |
| D4 | inject-operator | **recover 失败仍移除 finalizer** | `experiment_controller.go:88-91` | finalizer 一去 CR 即被 GC，脏注入永久留存在节点 |
| D5 | chaosmetad | **BaseInjector.Recover 对 error 状态短路返回 nil** | `pkg/injector/injector.go:128-130` | 注入若曾被标记 error，recover 变 no-op，故障持续驻留 |
| D6 | chaosmetad | **无启动期 stale-recovery 扫描** | `cmd/main.go`（无 startup scan） | 引擎重启后，`status=success` 但未恢复的记录无人清理 |
| D7 | chaosmetad | **无并发互斥**（同 uid 并发 inject/recover） | `ProcessRecover`/`ProcessInject` 无锁 | 并发竞争可致状态错乱/双重恢复 |
| D8 | chaosmetad | **无指标产出**（成功率/延迟/错误计数） | 全仓库无 metrics 注册 | 过程数据可视化无数据源 |
| D9 | platform | **UserStopExperiment 只翻 Argo workflow 状态，不确认 chaosmetad recover 真完成** | `routine.go:319-341` | "停止"只切状态，脏注入可能残留 |
| D10 | frontend | **两套不一致状态枚举**：`experimentStatus`(数字0-3) vs `experimentResultStatus`(字符串 Pending/Running/Succeeded/Failed/error)，且无 Paused/Stopped | `constants/index.ts:91-154` | UI 状态表达割裂、易错 |
| D11 | frontend | **日志静态展示**（AceEditor 一次性灌入），无 SSE/轮询、无级别过滤、无节点过滤 | `ExperimentResultDetail/ShowLog.tsx` | 运行中无实时日志 |
| D12 | frontend | **过程数据图表是注释掉的假数据占位** | `ExperimentResultDetail/ObservationCharts.tsx` | 无真实成功率/延迟分布/错误计数可视化 |
| D13 | frontend | **非 200 响应被静默吞掉** | `utils/errorHandler.ts` | 交互失败无显式反馈 |
| D14 | chaosmetad | **pprof 默认开启**（生产暴露） | `cmd/main.go` | 安全/爆炸半径风险 |

---

## 1. 任务1：前端页面重构

### 1.1 设计目标
- **简洁、有氛围感、现代**：统一设计语言（卡片化、留白、清晰的层级与配色），不沿用现有挤在一起的表格风格。
- **配置 / 执行状态 / 实时日志 / 过程数据** 四区清晰可读。
- **交互反馈显式**：启动/停止/暂停/恢复均带 loading / 成功 / 失败 三态反馈（D13 修复）。

### 1.2 页面结构（重构后）

```
故障注入（ExperimentDetail，重构）
├── ① 顶栏：实验名 + 状态徽标（统一枚举）+ 操作按钮组
│     [启动] [暂停/恢复] [停止] [复制配置] [删除]
│     每按钮：loading 态(禁用+spin) | 成功(message) | 失败(error toast)
├── ② 配置卡（折叠）：scope/rangeMode/selector/target/fault/args/duration 只读回显
├── ③ 执行状态卡：状态机时间线（idle→running→paused→stopped→error）
│     每个节点对象一行：target + 当前态 + message + 耗时
├── ④ 实时日志卡（核心，替换 ShowLog）：SSE/轮询流式 + 级别(info/warn/error) + 节点过滤 + 暂停滚动 + 导出
├── ⑤ 过程数据卡（替换 ObservationCharts 占位）：
│     - 注入成功率仪表盘
│     - 延迟分布直方图
│     - 错误计数折线/分类
│     - 节点维度明细表
└── ⑥ 历史结果入口（实验结果列表 + 结果详情，复用重构后的日志/数据组件）
```

### 1.3 状态枚举统一（修 D10）

新增统一枚举 `experimentRunStatus`，废弃 `experimentStatus`(数字) 与 `experimentResultStatus`(字符串) 的混用：

```ts
export const experimentRunStatus = {
  Idle:      { value: 'Idle',      label: '待执行', color: 'default' },
  Running:   { value: 'Running',   label: '运行中', color: 'processing' },
  Paused:    { value: 'Paused',    label: '已暂停', color: 'warning' },
  Stopped:   { value: 'Stopped',   label: '已停止', color: 'default' },
  Succeeded: { value: 'Succeeded', label: '成功',   color: 'success' },
  Failed:    { value: 'Failed',    label: '失败',   color: 'error' },
  Error:     { value: 'Error',     label: '异常',   color: 'error' },
} as const;
```

前端做一层 status adapter：把后端/CRD 的多源状态（CRD.StatusType、chaosmetad 4 态、Argo workflow 状态）映射到统一 8 态。

### 1.4 实时日志组件（修 D11）

`RealtimeLogPanel`（替换 ShowLog.tsx）：
- 数据源：新增平台 API `GET /chaosmeta/api/v1/experiments/:uuid/logs?instance=:id&level=&node=&since=&follow=1`，后端用 SSE（`Content-Type: text/event-stream`）推送；follow=0 时一次性返回历史。
- 后端聚合源：operator reconcile 日志 + chaosmetad 的 `/query`（含 status/message）+ 节点 daemonset pod 日志。
- 前端：`EventSource` 订阅；级别色（info 灰/warn 橙/error 红）；节点/级别过滤器；滚动暂停；虚拟列表（长跑日志量大）；导出按钮。
- **持久化**：日志落 DB（platform 侧 experiment_instance_log 表），后端从 DB 兜底返回历史，运行中也实时追加。

### 1.5 过程数据组件（修 D8、D12）

`MetricsPanel`（替换 ObservationCharts.tsx 占位）：
- 新增平台 API `GET /chaosmeta/api/v1/experiments/:uuid/metrics?instance=:id` 返回：
  `successRate`（成功率%）、`latencyDist`（p50/p90/p99/max 直方图桶）、`errorCounts`（按错误类型聚合）、`nodeBreakdown`（每节点 inject/recover 成功失败计数）。
- 数据源：chaosmetad 新增 metrics 端点（见 §2.6），platform 聚合后给前端。
- 图表：echarts5 仪表盘（成功率）+ 柱状图（延迟分布）+ 折线（错误计数时序）+ 表格（节点明细）。

### 1.6 交互反馈（修 D13）
- `utils/request.ts` 统一拦截：非 200 且未被业务层 `skipErrorHandler` 的，弹 `notification.error` 并写入全局错误态；按钮 Promise 链显式 cuyo loading→success/fail。

---

## 2. 任务2：核心故障注入逻辑增强

### 2.1 状态机设计（修 D1、D2）

#### 2.1.1 统一状态模型

用户/CRD/API 维度为 8 态；**引擎层（chaosmetad）只做纯新增**（`paused`），`success` 原样保留作「驻留中」语义，见 §2.1.3。状态机：

```
idle ──start──▶ running ──duration 到/手动 stop──▶ (recover 中) ──▶ succeeded/stopped
  │                │  ▲                               
  │                │  └──resume────────────────────────┤
  │                ├──pause──▶ paused ──stop/resume──▶ stopped/running
  │                │              
  │                │            (超时/异常)
  │                ▼              
  └──────────────▶ error ◀─────── stop 仍可从任意态（含 error）安全回到 idle（系统恢复到注入前态）
注: recovering 为 operator/平台侧的过渡标记（DB/CRD），引擎层不自持 recovering 态。
```

| 用户态 | CRD StatusType | chaosmetad status | 含义 |
|------|----------------|-------------------|------|
| Idle | `created` | `created` | 已创建未注入 |
| Running | `running` | `success`（驻留中）| 注入成功、故障当前驻留、duration 未到 |
| Paused | **新增 `paused`** | **新增 `paused`** | 用户暂停（仅进程类：SIGSTOP 驻留进程 + 停掉孤儿 timer） |
| Stopped | **新增 `stopped`** | `destroyed` | 用户停止，已清理 |
| Recovering(过渡) | **新增 `recovering`** | （过渡，不落 DB 特定态，= success→destroyed 之间）| 恢复进行中（CRD/平台层标记） |
| Succeeded | `success` | `destroyed` | 注入+恢复全成功结束 |
| Failed | `failed/partSuccess` | `error`/`destroyed` | 部分失败但已恢复 |
| Error | **新增 `error`** | `error` | 异常态，故障可能仍驻留，需人工或显式 stop 收尾（D5 修复后 stop 能真实 recover）|

> **暂停语义说明**：chaosmetad 现有驻留模型是"孤儿 sleep 进程延迟 recover"。暂停对**进程类注入**=对受影响进程发 SIGSTOP（驻留但不消费 CPU）+ 取消延迟 recover 定时器；对**内核态注入**（tc qdisc 丢包等）= 暂不支持真暂停，UI 禁用暂停按钮并提示"该故障类型不支持暂停"。这是务实的范围控制——不为内核态故障造一套不存在于 chaosmetad 的暂停原语。

#### 2.1.2 CRD 改动（`experiment_types.go`）

```go
const (
    CreatedStatusType     StatusType = "created"
    RunningStatusType     StatusType = "running"
    PausedStatusType      StatusType = "paused"      // 新增
    StoppedStatusType     StatusType = "stopped"     // 新增
    RecoveringStatusType  StatusType = "recovering"  // 新增（过渡）
    SuccessStatusType     StatusType = "success"
    PartSuccessStatusType StatusType = "partSuccess"
    FailedStatusType      StatusType = "failed"
    ErrorStatusType       StatusType = "error"       // 新增
)

// PhaseType 新增 pause（仅过渡，触发暂停动作）
const (
    InjectPhaseType  PhaseType = "inject"
    RecoverPhaseType PhaseType = "recover"
    PausePhaseType   PhaseType = "pause"   // 新增
)
```

> 改 CRD 需 `make manifests` 重生成 CRD yaml（chaosmeta-deploy 内），并升级 CRD 版本/兼容。

#### 2.1.3 chaosmetad 状态改动（`common.go`）—— **仅新增，不重命名、不迁移**

```go
const (
    StatusCreated   = "created"
    StatusSuccess   = "success"   // 保留：注入成功、故障当前驻留中（= 用户态 Running）。不重命名。
    StatusPaused    = "paused"    // 新增（仅进程类暂停：SIGSTOP 受影响进程 + 停掉孤儿定时器）
    StatusError     = "error"     // 保留：异常，故障可能驻留
    StatusDestroyed = "destroyed" // 保留：已清理（= 用户态 Stopped）
)
```

> **关键决策：不把 `success` 重命名为 `running`，不做 `success`→`running` 的启动期 DB 迁移。**
> 根据 `injector.go:282 / 293-298`，注入成功即写 `success` 并 fork 一个**脱离的孤儿 `sleep N; chaosmetad recover <uid>` 进程**——该孤儿**在 chaosmetad 主进程重启后仍然存活**，是定时 recover 的真正载体。`success` 是「故障当前驻留、duration 未到」的正常态。若启动期扫所有 `success` 去 recover（=「重启即清」），会把每一条还在投递窗口内的定时实验提前杀掉，且随后孤儿定时器到来会双重 recover——**这是灾难级爆炸半径破坏，明确禁止**。
>
> 因此：引擎层 `success` 原样保留语义（= 运行中/驻留）；用户可见的 `Running` 状态在 CRD/API/UI 用 adapter 映射 `success → Running`。`paused` 为**纯新增值**，旧数据无 `paused`，无需迁移。`error` 保留语义。
> §2.1.1 状态表已正确把 Running 映射到引擎 `success`，本节与之一致。

#### 2.1.4 controller `statusProcess` 调度

`experiment_controller.go:156` 的 switch 增补 `Paused/Stopped/Recovering/Error` 分支，挂接 phasehandler 的新 Solve 函数。

### 2.2 干净停止与恢复（修 D3、D4、D5、D9）—— **本任务最核心**

#### 2.2.1 停止的统一语义

> **铁律**：无论触发原因（用户停止 / 系统异常 / 网络断连 / 超时），停止后系统必须回到**注入前态**：故障驻留物（进程/连接/文件锁/内核态/qdisc）全部清除，chaosmetad 记录入 `destroyed`，CRD 入 `stopped`，操作句柄与孤儿 sleep 进程全清。

#### 2.2.2 controller 删除路径修复（D3、D4）

`experiment_controller.go:82-93` 重写：

```go
if !instance.ObjectMeta.DeletionTimestamp.IsZero() {
    // 任意非终态都强制触发 recover（D3：不再只限 success/failed/partSuccess）
    switch instance.Spec.TargetPhase {
    case v1alpha1.InjectPhaseType, v1alpha1.PausePhaseType:
        if instance.Status.Status != v1alpha1.StoppedStatusType { // 未停止则先 recover
            instance.Spec.TargetPhase = v1alpha1.RecoverPhaseType
            return ctrl.Result{Requeue: true}, r.Update(ctx, instance)
        }
    case v1alpha1.RecoverPhaseType:
        // D4 修复：recover 必须真正成功才移除 finalizer
        if instance.Status.Status == v1alpha1.SuccessStatusType || instance.Status.Status == v1alpha1.StoppedStatusType {
            solveFinalizer(instance)
            return ctrl.Result{}, r.Update(ctx, instance)
        }
        // recover 仍在进行或失败 → 不移除 finalizer，保持 CR 重排（operator 会重试 recover）
        // 失败计数到阈值后转 Error 态并告警，避免无限重排（见 §2.2.5）
        return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
    }
    return ctrl.Result{}, nil
}
```

要点：
1. **running/created 被删也触发 recover**（D3 修）。
2. **recover 失败不移除 finalizer**（D4 修），CR 留着持续重排，直到 recover 成功或转 Error。
3. 重间隔退避 + 失败计数，避免无限重排打爆 apiserver。

#### 2.2.3 chaosmetad Recover 短路修复（D5）—— **一行改动，不引入新抽象**

源码事实（`pkg/injector/injector.go:128-134`，已核）：

```go
func (i *BaseInjector) Recover(ctx context.Context) error {
    if i.Info.Status == utils.StatusDestroyed || i.Info.Status == utils.StatusError {
        return nil
    }
    return fmt.Errorf("not implemented")
}
```

而**全部 35 个子注入器**走的是「委托短路 + 内联清理」模式（`grep i.BaseInjector.Recover(ctx) == nil` 命中 35 处，含 `ppu/kill/loss/fdfull/...`）：

```go
func (i *FaultInjector) Recover(ctx context.Context) error {
    if i.BaseInjector.Recover(ctx) == nil {   // 基类短路命中 → 已清理/已error，子项直接返回 nil，不再清理
        return nil
    }
    // 否则执行真实的内联清理（kill 孤儿 sleep / tc qdisc del / ip rule del / 还原 marker…）
    return i.<inline-recover>(ctx)
}
```

因此「`error` 态故障永不恢复」的根因**就在基类的那个 `|| i.Info.Status == utils.StatusError`**：它让所有子注入器的 `BaseInjector.Recover()` 命中并短路返回 `nil`，跳过了真实清理。

**修复 = 从基类短路里删掉 `StatusError`**（一字之差）：

```go
func (i *BaseInjector) Recover(ctx context.Context) error {
    if i.Info.Status == utils.StatusDestroyed {
        return nil // 已清理，幂等
    }
    // 删除 StatusError 短路：error 意味着故障可能仍驻留，必须落到子注入器的真实清理
    return fmt.Errorf("not implemented") // 子注入器据此执行内联 recover
}
```

> 旧设计误把修复写成 `return i.recoverInternal(ctx)`——**`recoverInternal` 不存在**，且会绕过子注入器的委托短路链，无法编译。真实改动只有基类这一行；子注入器一行都不用改，短路链天然把 `error` 落到各子项的真实内联 recover。
>
> 幂等前提确认：各 injector 的内联 recover 对「本就没注入成功/已清理」普遍幂等（`kill`/`tc qdisc del`/`ip rule del` 对不存在目标返回成功）。实现期逐一核对 35 个 injector 的 recover 幂等性（尤其是 `ppu.*` 与 `mem/fill`），对不幂等的补 `if not exists` 守卫。

#### 2.2.4 platform Stop 确认 chaosmetad 真完成（D9）

`routine.go:UserStopExperiment` 重构为两段：

```
阶段1: Set CR TargetPhase=recover (触发 operator reconcile)
阶段2: 轮询 CR.Status.Status，直到 stopped/succeeded（recover 落地）或超时
  - 每个节点对象查 chaosmetad /query 确认 status=destroyed
  - 全部 destroyed → DB 标 WorkflowSucceeded(Stopped)
  - 有未 destroyed → 标 Error 并把未清节点写进 message，前端告警
返回: 仅当阶段2确认清理完成才对用户返回"停止成功"
```

平台状态机同步新增 `Stopping`(过渡) 态，前端在 Stopping 时按钮转 loading，完成或超时再翻终态。

#### 2.2.5 recover 重试与退避（防止无限重排）

- controller：recover 失败计数（写 `instance.Status.Message` 里的 retry 次数或 annotation），指数退避 `5s→10s→30s→60s`，到 `MaxRecoverRetry=8` 仍失败则转 `Error` 态并保留 finalizer + 前端告警。
- chaosmetad：`ProcessRecover` 加 uid 级互斥锁（D7 同源），避免并发双恢复。

### 2.3 异常中断恢复（修 D6）—— crash / 网络断 / 引擎重启

#### 2.3.0 前提：孤儿 timer 的可追踪化（为安全停 / 暂停 / 安全扫描铺路）

源码事实（`injector.go:285-298`，已核）：注入成功后用 `cmdexec.StartSleepRecover` fork 一个 **脱离的孤儿 `sleep N; chaosmetad recover <uid>` 进程**，主进程**不保存其 PID**、不记录其回收时刻；该孤儿**在 chaosmetad 重启后仍存活**，是定时 recover 的唯一载体。

不去追踪它就无法安全实现「停止」与「暂停」（需要停掉/暂停这个 timer 才能避免 duration 到点自动 recover 与人为动作冲突），也无法安全区分「定时器还在、别动」与「孤儿丢了、需兜底」。因此**新增可持久化追踪列**（`storage.Experiment` / SQLite 表 + gorm 标签）：

| 新列 | 类型 | 写入时机 | 用途 |
|------|------|----------|------|
| `orphan_pid` | int | `DelayRecover` fork 后存 pid | 精确 kill 该孤儿 timer（停止/暂停用）|
| `recover_deadline` | int64(unix) | `time.Now()+timeout` | 扫描判据：`now > recover_deadline` 时孤儿**本应已触发**，若 uid 仍 `success` 则判定「孤儿丢失/超时未 recover」|

> 向下兼容：旧记录这两列为空 → 启动扫描按「保守视为可能驻留」处理（见 §2.3.1 兜底分叉），**绝不主动 recover**。
> 暂停仅对进程类注入：暂停 = `SIGSTOP` 受影响进程 + `kill(orphan_pid)` 停掉自动 recover timer；恢复 = `SIGCONT` + 重新 fork 一个新孤儿 timer（按剩余时长 = `recover_deadline - now`）。

#### 2.3.1 chaosmetad 启动期 stale-recovery 扫描（**只救丢了 timer 的，绝不扫活着的**）

`cmd/main.go` 启动后新增 goroutine。**核心铁律：不动任何「还在定时窗口内」的 `success` 记录**，否则就成重启即清，灾难性破坏爆炸半径。

```
scanStaleExperiments():  // 启动后异步 + 周期（如每 5min）一次
  遍历 SQLite 所有记录
  - status == destroyed: 跳过
  - status == created (从未注入): 跳过
  - status == success (驻留中):
      if orphan_pid != 0 且该 pid 当前进程仍是 'sleep … chaosmetad recover <uid>' 形态:
          → 定时器还活着，绝对不动（记录健康，metrics ResidentGauge+1）
      else:  // 旧记录两列为空 / pid 已死 / 形态不符 = 孤儿丢了
          → 这是 D6 真正想兜底的对象：fork 的新孤儿可能在 crash 时被一起回收
          → 仅当 recover_deadline != 0 且 now > recover_deadline:
              timer 本该已触发却仍 success = 残留 → ProcessRecover(uid) 清理
          → 若 recover_deadline 未到：重新 fork 一个剩余时长的孤儿 timer 补上（续命，不清理）
          → 两者都判定不明确时（旧记录无 deadline）: 标记为「疑似驻留，待人工确认」并 metrics 上报，不主动 recover
  - status == error: 故障可能驻留 → 列「待人工确认」清单，metrics 上报，不主动 recover（D5 修复后可由显式 stop/stop-API 触发真实 recover）
  - status == paused: 不可能从跨重启恢复出原进程状态 → 列「待人工确认」，不主动动
```

> 这才是安全的 D6 兜底：**只救「孤儿 timer 丢了且已过 deadline」的真残留**，**绝不扫「定时器还活着的正常驻留」**。
> 残留孤儿收割（独立、与上面无依赖）：扫进程表里 `sleep N; chaosmetad recover <uid>` 形态，若其 `uid` 在 DB 已是 `destroyed`，收割该孤儿，防止僵尸进程累积。

#### 2.3.2 operator 侧 finalizer 兜底

operator 自身重启后，所有带 finalizer 的 running/error CR 会重新进入 reconcile → 命中 §2.2.2 修复后的删除/状态路径，自然触发 recover。无需额外代码，依赖 D3/D4 修复。

#### 2.3.3 网络断连

- chaosmetad 在节点上本地自治：duration 到了由孤儿 sleep 触发 recover，不依赖 platform 连通性。
- platform 重连后通过 CR 状态对账（轮询 /query + CR status），把 DB 状态校正。

### 2.4 日志数据结构（修 D11 持久化）

#### 2.4.1 platform 侧 DB 表

```sql
-- 实验实例日志（持久化，运行中与终态都可查）
CREATE TABLE experiment_instance_log (
  id            BIGINT AUTO_INCREMENT PRIMARY KEY,
  instance_uuid VARCHAR(64)  NOT NULL,
  experiment_uuid VARCHAR(64) NOT NULL,
  node          VARCHAR(128) NOT NULL DEFAULT '',   -- 节点/IP
  level         VARCHAR(16)  NOT NULL DEFAULT 'info', -- info|warn|error
  phase         VARCHAR(16)  NOT NULL DEFAULT '',     -- inject|recover|pause
  message       TEXT         NOT NULL,
  trace_id      VARCHAR(64)  NOT NULL DEFAULT '',
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  INDEX idx_instance_time (instance_uuid, created_at),
  INDEX idx_instance_node_level (instance_uuid, node, level)
);
```

#### 2.4.2 日志采集链路

```
operator reconcile 日志 ─┐
chaosmetad /query(stdout/stderr 容器日志) ─┼─▶ platform LogCollector(轮询/订阅) ─▶ DB ─▶ SSE ─▶ 前端
chaosmetad 业务日志(节点 daemonset pod) ─┘
```

采集方式（务实选择）：
- **阶段A（本任务交付）**：platform 主动轮询——定时拉 Argo node + chaosmetad /query，按节点聚合写 DB。实现简单、无新组件。
- **阶段B（后续）**：节点侧 chaosmetad 侧新增本地日志推送（daemon 向 platform 发日志批次），减轮询压力。本任务不含。

#### 2.4.3 日志级别溯源
- chaosmetad 现有 `log.Error` 即 error；`log.Info`/`Debug` 映射 info；新增 `log.Warn`→warn（`log.go:40` 已有 Error 常量，补 Warn）。
- operator `logger.Info/Error` 映射 info/error；reconcile 失败路径补 warn。

### 2.5 过程数据结构（修 D8、D12）

#### 2.5.1 chaosmetad metrics 注册

```go
// pkg/metrics/metrics.go (新增)
var (
    InjectTotal    = promauto.NewCounterVec(...)   // by target,fault,node
    InjectSuccess  = promauto.NewCounterVec(...)
    InjectFail     = promauto.NewCounterVec(...)
    InjectLatency  = promauto.NewHistogramVec(..., []float64{10,50,100,250,500,1000,2500,5000}ms)
    RecoverTotal   = promauto.NewCounterVec(...)
    RecoverSuccess = promauto.NewCounterVec(...)
    ResidentGauge  = promauto.NewGaugeVec(...)     // 当前驻留注入数
)
// /metrics 端点（与 pprof 收敛到 admin 端口或加鉴权，管 D14）
```

#### 2.5.2 platform 聚合 API

`GET /experiments/:uuid/metrics` 返回结构：

```json
{
  "successRate": 0.923,
  "total": 13, "succeeded": 12, "failed": 1,
  "latency": {"p50":120,"p90":480,"p99":1200,"max":2400,"buckets":[{"le":100,"v":3},...]},
  "errors": [{"type":"net_unreachable","count":1}],
  "nodes": [{"node":"10.0.0.2","inject":2,"recover":2,"fail":0}, ...]
}
```

数据源：platform 从 chaosmetad /metrics（Prometheus exposition）拉取并按实例过滤，或 chaosmetad 新增 `/v1/experiment/metrics?uid=` 业务端点直给 JSON（更轻，本任务选此）。

### 2.6 爆炸半径与隔离性（非功能）

- **爆炸半径**：所有改动严格限于故障注入链路（chaosmetad/inject-operator/platform 的 experiment 路由/frontend 的 Space/Experiment*）；不碰 flow-operator、measure-operator、core 业务路由。
- **向后兼容**：
  - CRD 新增 `paused/stopped/recovering/error` 等 StatusType 与 `pause` PhaseType；旧 StatusType 保留。旧 CR 被新 operator reconcile 时按映射表归一（`created→Idle, success→Running, failed→Failed, partSuccess→Failed, running→Running`）——纯显示层映射，不改 DB。
  - chaosmetad **不做状态重命名与 DB 迁移**（§2.1.3）：`success` 保留「驻留中」语义；新增 `paused` 为纯新增值，旧记录无 `paused`。新增 `orphan_pid`/`recover_deadline` 两列为空时按 §2.3.1 兜底分叉保守处理。
  - 前端 status adapter 兼容旧后端返回的 Pending/Running/Succeeded/Failed/error。
- **降级**：metrics 端点失败不阻断主流程，前端 metrics 区显示"数据不可用"而不崩。
- **pprof 收敛（D14）**：默认关 /debug/pprof，仅当 `--debug` 启动参数或 admin 端口开放。

### 2.7 极端场景处理矩阵

| 场景 | 处理 |
|------|------|
| 网络抖动(platform↔chaosmetad) | chaosmetad 本地自治 recover；platform 重连后对账；Stop 阶段2 超时→标 Error+未清节点列表 |
| 磁盘满(chaosmetad SQLite 写失败) | 返回 DBErr，inject 失败标 error，不 crashing；recover 仍尝试内存路径；日志采集失败降级丢弃并计数告警 |
| 权限不足(nsenter/container exec) | inject 返回权限错误→标 failed，UI 显式报"节点权限不足:<node>"；不重试无意义操作 |
| 并发冲突(同 uid 并发 inject/recover) | chaosmetad uid 级互斥锁(D7)；CRD 用 resourceVersion 乐观锁；重复请求返回 409 |
| chaosmetad 重启 / OOM kill | §2.3.1 启动期 stale scan 兜底；孤儿 sleep 进程收割 |
| operator 重启 | 带 finalizer 的 CR 重排触发 §2.2.2 路径自愈 |
| 7×24h 长跑内存/句柄泄漏 | chaosmetad 无内存句柄模型(孤儿进程)，主进程恒定；metrics ResidentGauge 监控；日志采集用有界队列+丢弃策略；前端 EventSource 断线自动重连 |
| recover 死循环 | §2.2.5 失败计数+指数退避+MaxRetry 转 Error，不无限重排 |

---

## 3. 测试计划

### 3.1 单元测试（须 Linux 构建，chaosmetad 部分）

| 层 | 用例 | 断言 |
|----|------|------|
| chaosmetad | `ProcessRecover` 对 status=error 不再短路(D5) | 实际执行 recover，孤儿进程被收割 |
| chaosmetad | uid 并发 recover(D7) | 第二个返回锁冲突/幂等成功，无双恢复 |
| chaosmetad | stale scan(D6) | 1)孤儿还活(success+pid形态对)→不动；2)旧记录无 deadline→不主动动、待人工；3)pid丢且过 deadline→recover 成 destroyed |
| chaosmetad | stale-scan 安全判据(§2.3.1) | 扫活着的 `success` 标记为误杀 | AUDIT-7
| inject-operator | 删除 running CR(D3) | 触发 recover，不直接删 finalizer |
| inject-operator | recover 失败(D4) | finalizer 保留，重排 |
| inject-operator | recover 失败达 MaxRetry | 转 Error 态 |
| platform | StopExperiment 阶段2(D9) | 全 destroyed 才标成功，否则标 Error+未清节点 |
| frontend | status adapter | 8 态映射正确，旧值兼容 |

### 3.2 集成 / dev 环境验证（全场景覆盖）

> **前提**：chaosmetad 构建在 Linux/集群；本地 macOS 仅做前端 + platform 后端单测。

**正常流**：
1. 创建实验 → 启动 → 注入驻留 → duration 到 → 自动 recover → Succeeded。
2. 创建 → 启动 → **暂停**（进程类）→ 驻留进程 SIGSTOP → **恢复** → 继续 → 停止 → Stopped。
3. 实时日志实时刷新、级别过滤、节点过滤生效。

**异常流**：
4. 启动中 **删除 CR**（D3）→ 确认 recover 被触发，节点故障清除。
5. 启动中 **kill chaosmetad** 引擎 → 重启 → stale scan 只 recover「孤儿 timer 丢了且已过 deadline」的真残留；对「定时器还活」的成功实验**不误杀**（D6 安全边界）。
6. recover 故意失败（mock 节点失联）→ finalizer 保留→重试→MaxRetry 转 Error（D4/§2.2.5）。
7. 同实验并发启动两次 → 第二次 409（D7）。

**极端流**：
8. chaosmetad 节点磁盘灌满 → inject 失败优雅降级，不 panic。
9. 网络断连 platform↔chaosmetad → Stop 阶段2 超时→标 Error+未清节点，UI 告警。
10. 权限不足节点 → inject 标 failed，UI 报"节点权限不足"。

**长跑**：
11. 7×24h 混合注入（每 5min 一次 inject + 偶发 kill chaosmetad）→ ResidentGauge 不单调累加，主进程 RSS 恒定，无孤儿进程累积。

### 3.3 验收闸门
- 所有 §3.1 单测绿（Linux）。
- §3.2 11 条全过，产出测试报告（每条：步骤/输入/期望/实际/截图或日志）。
- 前端：任务1 四区视觉显著优于现状，交互三态反馈齐全。
- 自审通过：实现对照 §4 影响面 + §0 缺陷清单逐条核对，D5/D6 安全边界无回退；爆炸半径守住（仅故障注入链路、对 flow/measure operator 与核心业务零侵入）。

---

## 4. 变更影响面（清单）

| 模块 | 文件 | 改动类型 |
|------|------|----------|
| chaosmetad | `pkg/utils/common.go` | 状态常量扩展(D2) |
| chaosmetad | `pkg/injector/injector.go` | Recover 短路修复(D5)、ProcessInject/Recover 互斥(D7) |
| chaosmetad | `cmd/main.go` | 启动 stale scan(D6, 仅救丢 timer 真残留)、pprof 收敛(D14)、**无状态迁移** |
| chaosmetad | `pkg/metrics/`(新) | metrics 注册与端点(D8) |
| chaosmetad | `pkg/log/log.go` | 补 Warn 级别 |
| chaosmetad | `pkg/web/`(handler/routers) | 新增 /metrics / /experiment/metrics / 日志接口 |
| inject-operator | `api/v1alpha1/experiment_types.go` | 状态/Phase 扩展(D1) |
| inject-operator | `controllers/experiment_controller.go` | 删除路径修复(D3/D4)、statusProcess 调度 |
| inject-operator | `pkg/phasehandler/inject/recover/handler.go` | 暂停/恢复/recovering Solve 函数 |
| chaosmeta-deploy | CRD yaml | 重新生成（新状态字段） |
| platform | `pkg/service/experiment/routine.go` | Stop 两段式(D9) |
| platform | `routers/` + 新 controllers | 日志 SSE / metrics API / 状态新枚举 |
| platform | DB migration | `experiment_instance_log` 表 |
| frontend | `src/constants/index.ts` | 统一状态枚举(D10) |
| frontend | `ExperimentResultDetail/ShowLog.tsx`→`RealtimeLogPanel` | 实时日志(D11) |
| frontend | `ExperimentResultDetail/ObservationCharts.tsx`→`MetricsPanel` | 过程数据(D12) |
| frontend | `utils/request.ts`/`errorHandler.ts` | 交互反馈(D13) |

---

## 5. 约束与风险

1. **macOS 构建限制**：chaosmetad（及依赖它的 inject-operator 的部分单测）必须在 Linux 编译运行。本任务的 dev 验证需 Linux 节点 / 集群。前端 + platform 后端可本地验证。
2. **CRD 升级**：新增 CRD 字段需在集群 `kubectl apply` 升级 CRD；旧实验 CR 由 operator 归一映射，不丢数据。
3. **暂停范围**：仅进程类故障支持真暂停；内核态故障暂停 UI 禁用并提示。范围受限以防造一套 chaosmetad 不存在的暂停原语。
4. **启动 stale scan 的安全边界（取代原「状态迁移」风险）**：旧记录无 `orphan_pid`/`recover_deadline`。**绝不**对这类「无法判定定时器是否还活」的 `success` 记录主动 recover——否则越权删除「本应继续驻留」的实验，违反爆炸半径。处置留「待人工确认」+ metrics 上报，由人决定。这是 D6 兜底的安全闸，本任务锁死不破。
5. **D5 改动风险**：error 态不再跳过 recover 是正确性修复，但要求每个 injector 的 recover 对"未真正注入"幂等。实现期逐一核对各 injector 的 recover 幂等性。

---

## 6. 执行顺序（与 /goal 阑流对齐）

1. ✅ 排查（本文档 §0）
2. ✅ 方案设计（本文档 v1）
3. ✅ 自审方案修订（本版 v2：修 D5 `recoverInternal` 编译错、D6 stale-scan 误杀活实验、引擎状态不重命名不迁移——三处爆炸半径硬伤）
4. ⏭ 编码实现（按 §4 影响面：chaosmetad 引擎 → operator → platform → frontend，逐层 + 单测）
5. ⏭ 自测 + dev 环境全场景（§3.2）迭代到无新 bug
6. ⏭ 自审代码（对照缺陷清单逐条核对、爆炸半径复核；必要时用本地审查 agent 增信）
7. ⏭ 合并推送（分支名含 `fault-injection-enhance`）
