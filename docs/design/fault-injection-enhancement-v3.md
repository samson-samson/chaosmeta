# chaosmeta 故障注入模块优化方案 — v3 增量

> 版本: v3 (2026-07-18)
> 基础: 续做已推送的 `940e9b9`（v2 完成 D1–D14 大部分），本版只补经真实代码核实后仍存在的 5 个真实缺口。
> 配套: 见同目录 `fault-injection-enhancement.md`（v2 全量设计，本文不重复）。
>
> **排查方法**: v2 报告自述跳过了 codex 审查、并把多处标为"降级/未验"。本版第一原则是**不盲信报告**：逐个读真实代码 + 亲自交叉编译（`GOOS=linux` chaosmetad exit=0 实测）核实后才定缺口。

---

## 0. 经真实代码核实的完成度（v2 已交付，确认非空壳）

逐条读真实源码 + 路由 + 编译验证后的结论（`file:line` 均为分支 `worktree-fault-injection-frontend-polish` HEAD）：

| v2 缺陷 | 真实状态 | 证据 |
|---|---|---|
| D1 无 paused/stopped/error | ✅ 真加 4 个 StatusType + PausePhaseType | `chaosmeta-inject-operator/api/v1alpha1/experiment_types.go:60-79` |
| D2 chaosmetad 仅 4 态 | ✅ 真加 `StatusPaused`，保留 `success` 语义 | `chaosmetad/pkg/utils/common.go:62-66` |
| D3 删除路径不覆盖 running/created | ✅ solveDeletion fall-through 真改 | `chaosmeta-inject-operator/controllers/experiment_controller.go` §2.A |
| D4 recover 失败仍移 finalizer | ✅ 仅 clean 终态移 | 同上 |
| D5 BaseInjector.Recover 短路 error | ✅ **真删** `StatusError` 短路 | `chaosmetad/pkg/injector/injector.go` Recover() |
| D6 无启动期 stale-scan | ✅ **真实现** `RunStaleScanLoop` + 安全判据 | `chaosmetad/pkg/injector/stale_scan.go` |
| D7 无并发互斥 | ✅ **真实现** `uidMutex` + `DelayRecoverWithPid`(已入 `IInjector` 接口) | `chaosmetad/pkg/injector/injector.go:45,68,227` |
| D9 Stop 只翻 Argo | ✅ **真实现** `confirmRecoverCompleted` 两段式轮询 + unclean escalate | `chaosmeta-platform/pkg/service/experiment/routine.go:333-409` |
| D10 前端两套状态枚举 | ✅ **真统一** 8 态 + `normalizeRunStatus` adapter | `chaosmeta-platform-frontend/src/constants/index.ts:169-323` |
| D11 日志静态 | ✅ 前端 SSE+poll 真组件 + 后端 logs 路由/model ｜ ⚠️ follow=1 降级返持久化集 | `RealtimeLogPanel.tsx` + `experiment_instance.go:141` |
| D12 过程数据假数据 | ✅ 前端 echarts 真组件 + 后端 metrics 真聚合（latency nil 诚实降级） | `MetricsPanel.tsx` + `experiment_instance.go:163` |
| D13 非 200 静默 | ✅ 前端组件全 surface | useExperimentAction / panels |
| D14 pprof 默认开 | ✅ `--enable-pprof` 默认 false | `chaosmetad/cmd/server/server.go` |
| 交叉编译 | ✅ 本人亲测 chaosmetad exit=0 (39M ELF) | `/tmp/cm-fi-verify-linux` |
| daemon 端到端 / 单测 | ✅ d5_d6_d7_test.go + daemon 4 场景（报告证） | `chaosmetad/pkg/injector/d5_d6_d7_test.go` |

**结论：v2 是真做了一大份实打实的工作，不是假实现。** 剩余 5 个缺口是 v2 自承的"降级/未接"项，且经我核实属实。

---

## 1. v3 要补的 5 个真实缺口

| # | 缺口 | 层 | 严重度 | 与验收标准对应 |
|---|---|---|---|---|
| **G1** | `ExperimentRunPanel` + `useExperimentAction`（启动/暂停/恢复/停止四按钮主操作面板）**src 全仓零引用**——`ExperimentResultDetail` 仍走旧 `Modal.confirm`+`stopExperimentResult`，不统一三态反馈；暂停/恢复按钮永远 404 | 前端 | 🔴 高 | 任务一"交互操作直观流畅"+"明确反馈" |
| **G2** | operator `statusProcess` switch **只有 5 个 case**（Created/Running/Success/PartSuccess/Failed），新四态 Paused/Stopped/Recovering/Error / PausePhaseType 无 case——CRD 常量加了但 reconcile 不识别（v2 §2.B 自承降级） | 后端 operator | 🟡 中 | 任务二"状态机状态迁移正确"+"暂停/恢复" |
| **G3** | chaosmetad metrics 端点（D8）**未实现**；platform metrics 从 workflow node status 降级聚合，`latency` 永远 nil | 后端 chaosmetad | 🟡 中 | 任务二"过程数据可视化" |
| **G4** | 前端仍是 antd 原生 Card/Button + 内联硬编码色，无设计系统/Token/暗色适配，观感模板化 | 前端 | 🟡 中 | 验收"美观度显著优于原版" |
| **G5** | codex 两步审查跳过；6 条 K8s/Argo 场景未跑 | 流程/测试 | ⚠️ 流程 | 验收"codex 两次审查无重大问题"+"测试报告" |

---

## 2. 方案：G1 接线主操作面板 + 统一三态反馈

### 2.1 现状精确定位

- `ExperimentRunPanel.tsx`（5103 字节，四按钮 + 三区）与 `useExperimentAction.ts`（统一 start/stop/pause/resume 三态 hook）**被造出来却没被任何页面 import**（`grep -rn ExperimentRunPanel src/` 除组件目录外零命中，已实测）。
- `ExperimentResultDetail/index.tsx:100-146` 仍是旧停止路径：`stopExperiment` useRequest + `Modal.confirm`，**不显示 loading/不区分网络错误**。
- `useExperimentAction` 现在对 pause/resume 直发 POST 到 `/experiments/:uuid/pause|resume`，后端无此路由 → 必 404 → 弹"接口未就绪"warning（v2 设计自承）。

### 2.2 方案

**2.2.1 在 `ExperimentResultDetail/index.tsx` 接入 `ExperimentRunPanel`**

- 顶部新增一卡，渲染 `<ExperimentRunPanel experimentInstanceUUID={resultDetail.uuid} status={resultDetail.status} onStatusChanged={() => 重拉详情} />`，替代旧 `headerExtra` 里纯 `停止` 按钮 + `Modal.confirm`。
- 保留旧 `stopExperiment` useRequest 作内部复用，但按钮逻辑改走 `useExperimentAction.run('stop')`，得到 loading 态 + 成功 toast + 失败 notification（D13）。
- 启动按钮：当 `resultDetail` 为已完成实例时不显示启动（避免对历史实例误启动）；仅当处于 `Idle/terminal` 且实验为 manual 触发时启用——沿用 v2 `ExperimentRunPanel` 内 `disabled={normalized !== 'Idle' && !terminal}`。

**2.2.2 修 `useExperimentAction` 的 uuid 语义**

- 后端真实路由（`routers/experiment.go`）：`POST /experiments/:uuid/start` 与 `POST /experiments/:uuid/stop`，`:uuid` 是**实验 uuid**。
- 当前 `ExperimentRunPanel` props 同时声明 `experimentUUID` 与 `experimentInstanceUUID`，但解构只取了 `experimentInstanceUUID` 并把它当路由参数 → 停止/启动会 404。
- 修：组件解构补上 `experimentUUID`，`act()` 传 `experimentUUID` 给 `useExperimentAction.run`；`ExperimentResultDetail` 实例有 uuid 也有 experimentUUID（`experimentInstanceInfo.ExperimentUUID`），前端详情接口需返回两者——检查 `GetExperimentInstanceDetail` 是否返回 experimentUUID，若否则补字段。

**2.2.3 pause/resume 接线（见 §3 同步后端）**

- 前端按钮在后端 pause/resume 路由就绪后自然生效；未就绪时仍 404 warning（v2 已有的优雅降级）。
- 暂停对内核态故障禁用：`ExperimentRunPanel` 已有 `disabled={normalized !== 'Running'}` 基线，但需细化为"仅进程类故障可暂停"。后端 pause 接口对非进程类返回明确 422 + 前端 tooltip，避免用户误操作。

### 2.3 验收点

- 进入实验结果详情页 → 顶部出现四按钮主操作卡，按钮 loading 成功 toast 失败 notification 三态齐全。
- 停止操作真触发后端 stop（HTTP 200 且重拉状态变 Stopped/Error）。
- pause/resume：后端就绪时真生效；未就绪时 warning 不崩溃。
- `grep ExperimentRunPanel src/` 命中至少 1 个页面引用。

---

## 3. 方案：G2 operator statusProcess 接入新四态

### 3.1 现状精确定位

- `experiment_controller.go:161-173` `statusProcess` switch：仅 `Created/Running/Success/PartSuccess/Failed`。
- 新四态 `Paused/Stopped/Recovering/Error` + `PausePhaseType` 已在 CRD 定义，但**无 reconcile 分支** → 写入这些状态的 CR 会落入默认分支（不处理）。
- v2 §2.B 自承"降级为潜在功能缺口，当前删除恢复路径走五态不触发"——**但任务验收要求"状态机状态迁移正确，停止操作总能恢复初始状态"，pause/resume 是任务一明确列出的交互**。必须在 v3 真接。

### 3.2 方案（务实范围控制：只接能闭环的最小集）

**核心原则**：不为内核态故障造一套 chaosmetad 不存在的暂停原语；让"停止"成为唯一从任意态安全收敛的兜底动作（与 v2 §2.1.1 一致）。

**3.2.1 `statusProcess` 增补 case**：

```go
switch instance.Status.Status {
case v1alpha1.CreatedStatusType:
    // …（不改）
case v1alpha1.RunningStatusType:
    // … 不改；注入驻留中
case v1alpha1.SuccessStatusType:
    // … 不改
case v1alpha1.PartSuccessStatusType:
    // … 不改
case v1alpha1.FailedStatusType:
    // … 不改

// ===== v3 新增 =====
case v1alpha1.PausedStatusType:
    // 暂停态：故障驻留但已发 SIGSTOP + 停掉孤儿 timer。reconcile 不主动推进，
    // 等用户 resume（TargetPhase 设回 inject、Status 设回 running）
    // 或 stop（TargetPhase 改 recover → fall-through 到恢复链）。只做幂等 record，不 requeue。

case v1alpha1.RecoveringStatusType:
    // 过渡态：已在 recover 链路上，让 RecoverPhaseHandler 推进；不在此再改 TargetPhase。

case v1alpha1.StoppedStatusType, v1alpha1.ErrorStatusType:
    // 终态/异常态：clean 终态 → 移 finalizer 让 GC；
    // 异常态(StatusError)若仍有 finalizer 且 DeletionTimestamp 未设 → 不移 finalizer（故障可能驻留），
    //   由前端"停止"按钮显式改 TargetPhase=recover 触发恢复后才能清。
    if instance.Status.Status == v1alpha1.StoppedStatusType {
        // 已确认清理 → 可移 finalizer（若删态入口已做过则幂等）
    }
    // ErrorStatusType: 默认不移，保留 finalizer 防止脏注入被 GC
}
```

**3.2.2 新增 phase handler**：在 `phasehandler/` 下为 `pause` 动作定义最小 handler。**范围限定进程类故障**：
- pause = 对驻留进程发 SIGSTOP（chaosmetad 现有能力）+ kill 孤儿 timer（`orphan_pid` 已在 v2 加了持久化列）。
- resume = SIGCONT + 重新 fork 剩余时长孤儿 timer。
- 内核态故障（tc qdisc / ip rule / 网络丢包）不支持 pause，handler 返回明确"unsupported"错误，operator 把 CR 留 Running 态并写 message，前端据此禁用按钮。

**3.2.3 platform pause/resume REST 路由**：
- 新增 `POST /chaosmeta/api/v1/experiments/:uuid/pause` 与 `/resume`（对齐 start/stop）。实现在 `experiment.go` controller，调 `ChaosmetaService` patch CR 的 `Spec.TargetPhase` / `Status` 触发 operator reconcile。
- 仅对 inject 类 CR 生效；flow/measure 维持现有 `Spec.Stopped` 不动（爆炸半径：不动其他 operator）。

### 3.3 风险与防护

- **CRD yaml 重生成**：新四态字段需在 CRD schema 中声明，否则 apiserver 校验拒绝。报告记 controller-gen 在 Go1.26 崩——**v3 实测能否跑通 controller-gen；若不行手工补 CRD schema 的 enum 枚举**（chaosmeta-deploy）。
- **单测**：`controllers/experiment_controller_test.go` 增补"删态 Paused → stop 触发 recover"、"Running → pause → Stopped"路径的纯函数分支断言（环境受限时同 v2 范式：交叉编译 + alpine 沙箱）。
- **运行时验证**：envtest 缺失的情况下，§3.2 的控制流以静态推演 + 纯函数单测覆盖，诚实标注未做集群实测（G5 待集群）。

### 3.4 验收点

- `grep "case v1alpha1.PausedStatusType\|case v1alpha1.ErrorStatusType" experiment_controller.go` 命中。
- 单测覆盖"任意态 → stop → recover 链可达 clean 终态"。
- 前端 pause/resume 按钮在后端就绪后真生效；内核态禁用且提示。

---

## 4. 方案：G3 chaosmetad metrics 端点（D8 真实现）

### 4.1 现状精确定位

- `experiment_instance.go:163-240` `GetExperimentInstanceMetrics` 已真聚合 workflow node status → successRate / total / succeeded / failed / errors / nodes，但 `resp.Latency` **永远不赋值**（指针留 nil）→ 前端延迟图恒为空。
- chaosmetad 无 `/v1/experiment/metrics` 业务端点（v2 设计 §2.5.2 原设想"chaosmetad 直给 JSON"未做）。
- chaosmetad `storage` 层已有 `Experiment` 记录的 `create_time/update_time`，可算每次 inject/recover 耗时。

### 4.2 方案（务实的轻量端点，不引 Prometheus 去重依赖）

**4.2.1 chaosmetad 新增业务端点**：`GET /v1/experiment/metrics?uid=` 或按实例聚合的 `/v1/experiment/metrics?target=&fault=&since=`，返回 JSON：
```json
{
  "inject": {"total":N,"success":M,"fail":K},
  "recover": {"total":N,"success":M},
  "latency_ms": {"p50":..,"p90":..,"p99":..,"max":..},   // 从 Experiment 记录 update_time-create_time 算
  "resident": R
}
```
- 数据源：`storage.QueryByOption` 拉实验记录，本地算成功率 + 延迟分位数（纯 Go，不引 Prometheus client）。延迟 = inject 成功时刻 − record create_time，近似可行（chaosmetad 现有字段）。
- 路由注册在 `chaosmetad/pkg/web/`，复用既有 handler 模式。

**4.2.2 platform 聚合调 chaosmetad**：`GetExperimentInstanceMetrics` 在拿到各节点 chaosmetad metrics 后：
- 填 `resp.Latency`（p50/p90/p99/max 从各节点的 latency_ms 合并）。
- successRate 从 chaosmetad 上更准（单机真计数），workflow node status 作交叉校验。

**4.2.3 单测**：`chaosmetad/pkg/web/` 或 `pkg/metrics/` 新增纯函数测试：给定一组 Experiment 记录，算出的 latency 分位数/成功率正确。**chaosmetad 单测同 v2 范式交叉编译 + alpine**。

### 4.3 风险

- chaosmetad 按 uid 查只覆盖单机，跨节点聚合由 platform 做——与 v2 §2.5.2 选"chaosmetad 直给 JSON"一致，更轻。
- 若 chaosmetad 不可达：platform 降级回 workflow node status 聚合（v2 现有路径不变），latency nil，前端显示"暂不可用"——**降级不崩溃**。

### 4.4 验收点

- 前端延迟分布图在有真实注入后显示数值（不再是恒占位）。
- chaosmetad 不可达时前端显示"暂不可用"而非空白/报错。
- metrics 端点单测绿。

---

## 5. 方案：G4 前端美观度打磨（设计系统化）

### 5.1 现状精确定位

- 全前端无 theme/Design Token：`src/app.tsx`、`src/layouts/index.tsx` 有 `ConfigProvider` 但只设 locale。
- 新组件全是 antd 原生 `Card`/`Button`/`Space` + 内联 `style={{}}`，硬编码色 `#ff5c5c`/`#1e1e1e`/`rgba(255,255,255,0.45)`。
- 验收硬要求"美观度显著优于原版"。

### 5.2 方案（轻量级设计令牌，不引第三方设计系统，零侵入）

**5.2.1 新增设计令牌文件** `src/theme/fi-tokens.ts`（故障注入专用，不动其他页）：
```ts
export const fiTokens = {
  surface:        '#ffffff',
  surfaceAlt:     '#f7f8fa',
  border:         '#e8ecf0',
  textPrimary:    '#1c2533',
  textSecondary:  'rgba(28,37,51,0.55)',
  statusRunning:  '#1677ff',
  statusPaused:   '#fa8c16',
  statusSucceed:  '#52c41a',
  statusFailed:   '#ff4d4f',
  statusError:    '#cf1322',
  logBg:          '#0f1115',
  logText:        'rgba(255,255,255,0.85)',
};
```

**5.2.2 在 `app.tsx` 的 `ConfigProvider` 注入主题**（仅运行态主题，可选暗色钩子）：
- 用 antd 5 的 `theme.token` 覆盖圆角、色板，统一故障注入页观感。
- **不**改全局背景，避免影响其他页（爆炸半径）。

**5.2.3 重构 `ExperimentRunPanel` 主面板**为更现代的布局：
- 顶栏换为"状态卡 + 操作区"：左侧大号状态徽标 + 进度提示，右侧按钮组用 `Space.Compact`。
- 增加状态机时间线（idle→running→paused→stopped→error）mini 图（已有 RunStatusBadge，可扩成 5 步时间线），让"状态迁移"可视化（对应任务二可观测）。
- 配置卡 ②：把 scope/rangeMode/selector/target/fault/args/duration 折叠区做成 Descriptions 只读回显，简洁分层。

**5.2.4 组件内联色替换为 token**：`MetricsPanel`、`RealtimeLogPanel` 里 `#ff5c5c`/`#5b8ff9` 等改为 `fiTokens.status*`，集中维护。

**5.2.5 暗色适配（可选轻量）**：通过 `media (prefers-color-scheme: dark)` 在 `global.less` 给 fi-tokens 做覆盖；不引 CSS-in-JS 主题运行时，零运行时开销。

### 5.3 风险

- 改 `app.tsx` ConfigProvider 影响全局——**只注 token、不改 layout/背景** + 本地 dev 实测各主要页（列表/创建/结果）不破。
- 前端 dev 联调受限于 umi 启后端代理——用 Playwright MCP 起本地前端 + mock 后端验证视觉（G5 验证手段之一）。

### 5.4 验收点

- 实验结果详情页视觉：顶栏操作卡 + 状态时间线 + 日志暗色终端 + 指标图卡，层次清晰，显著优于原版表格罗列。
- 其他页面（实验列表/创建）无回归（视觉 diff 靠 Playwright 截图比对）。
- 硬编码色清零（grep `#ff5c5c` 在故障注入组件内为 0）。

---

## 6. 方案：G5 codex 审查 + 测试报告

### 6.1 两次 codex-reviewer

- **第 1 次（方案阶段，本版发布后）**：把本 v3 文档提交 codex-reviewer 审"方案是否合理、缺口判断是否准确、范围控制是否守住爆炸半径、是否漏掉真缺口"。据反馈定稿。
- **第 2 次（代码阶段，实现+测试后）**：把最终 diff 提交 codex-reviewer 审"有无 bug/逻辑漏洞/资源泄漏/并发问题/爆炸半径回退"。修复所有发现。

### 6.2 测试矩阵（v3 在 v2 基础上增补）

| 测项 | v3 范围 | 验证手段 |
|---|---|---|
| 交叉编译四层 | 续 v2 | 本人重跑 `GOOS=linux go build` 四层 exit=0 |
| 单测 d5/d6/d7 + 新增 metrics/单测 | chaosmetad web/metrics + operator pause 分支 | 交叉编译 `go test -c` + alpine 沙箱 |
| daemon 端到端 inject/recover/stale/mutex | 续 v2 4 场景 | alpine + chaosmetad server + wget |
| 前端四按钮主面板接线 | **新增** | Playwright MCP 起本地前端 mock 后端，点启动/停止/pause/resume 三态反馈截图 |
| metrics 延迟真数值 | **新增** | 注入后前端延迟图非空 |
| K8s/Argo 6 场景 | 本机不具备集群 | **静态推演 + 纯函数单测覆盖，诚实标注未做集群实测** |
| 7×24h 长跑 | 本机不具备 | 孤儿 timer 跨重启存活 v2 已证；RSS 监控标"待集群" |

### 6.3 测试报告更新

- 更新 `docs/fault-injection-enhance-test-report.md` 加 v3 章节：G1 接线验证、G2 单测、G3 metrics 端点、G4 视觉对比图、codex 两次审查结果。

---

## 7. 爆炸半径与隔离性复核

- **改动范围严格限于故障注入链路**：前端 `Space/ExperimentResultDetail` + `components/ExperimentRun` + `theme/fi-tokens`；后端 `experiment_instance.go` metrics 聚合 + chaosmetad `pkg/web` metrics 路由；operator `experiment_controller.go` switch + `phasehandler/pause`。
- **不动**：flow-operator、measure-operator、platform 的 user/space/kubernetes 路由、前端其他 Space 页。
- **向后兼容**：新四态 CRD 是纯新增 enum，旧 CR 走 5 态不变；chaosmetad 不重命名 status；platform metrics 聚合降级路径保留。
- **外向操作**：git push 待 dev 验证 + codex 无重大问题后执行（已授权），分支名带 `fault-injection-polish-v3` 后缀，按白名单分类（业务码可推、临时产物/`/tmp` 验证产物不推）。

---

## 8. 执行顺序（接续 v2 的流程）

1. ✅ 排查（v2 §0 + 本版 §0 真实代码核实）
2. ✅ 方案设计（本 v3 文档）
3. ✅ 第 1 次 codex-reviewer 审方案（见 §9 修订）
4. ⏭ 编码：**G0 webhook/routine 双解** → G1 接线 → G4 前端打磨 → G3 chaosmetad metrics → G2 operator switch + pause handler → 各层单测
5. ⏭ 自测：§6.2 测试矩阵，本地交叉编译 + alpine + Playwright
6. ⏭ 第 2 次 codex-reviewer 审代码（G5.6.1）
7. ⏭ 创建分支 + push 到 origin（已授权）

---

## 9. 第 1 次 codex 审查反馈 → v3.1 修订（2026-07-18）

codex-reviewer 读真实代码独立核实后，发现 4 条 P1（致命）+ 2 条 P2。本人已逐条亲证属实（`file:line` 见下）。本节是对正文章的**正式修订**——编码以本节为准。

### 9.1 【G0 新增·最高优先级·前置阻塞】webhook + routine 双重拦截，使 stop/pause 对 running/error 态发不出去

**亲证 1（webhook 铁壁）** — `chaosmeta-inject-operator/api/v1alpha1/experiment_webhook.go:186-188` ValidateUpdate：
```go
if !(oldExp.Status.Phase == InjectPhaseType && (Status == SuccessStatusType || FailedStatusType || PartSuccessStatusType)) {
    return fmt.Errorf("only support update when \"status.phase == inject and status.status == success/failed/partSuccess\"")
}
```
→ **Running 态 / Error 态 / Paused 态 的 CR 改 TargetPhase 全被 webhook 拒**。这意味着：
- 平台发 recover（改 TargetPhase=recover）对"正在注入"的实验**发不出去**——必须等 duration 自然结束。
- pause/resume 设计完全无法落地（pause 需改 Spec.Status，webhook 连 targetPhase 都只允许 success/failed/partSuccess 时改）。
- **940e9b9 加的新四态常量 + v3 原文 G2 的 switch case 是空架子**——reconcile 永远收不到这些状态的 CR 更新。

**亲证 2（routine 拦截）** — `chaosmeta-platform/pkg/service/experiment/routine.go:433-435` UserStopExperiment：
```go
if Status == WorkflowSucceeded || WorkflowFailed || WorkflowError {
    return errors.New("experiment is over")
}
```
→ Error 态实例被 routine 层直接拒绝停止（不止前端按钮 disabled）。

**方案 G0（务必在 G1/G2 前做，否则全是空架子）**：
1. **webhook 有条件放开**：`ValidateUpdate` 增补"允许从 Running/Paused 态改 TargetPhase 为 recover（=stop）/pause"。安全约束：
   - 只允许 `targetPhase` 变更，禁改 `Experiment/Selector/RangeMode/Scope`（现状已禁）。
   - 只允许 `inject→recover` 与 `inject→pause`（pause 仅进程类）；**禁止** `recover→inject` 等回退防注入复活。
   - 暂停对内核态 CR：operator 侧 handler 返回 unsupported，CR 留 Running + 写 message（不改 webhook 规则，云端不区分故障类型）。
2. **routine 有条件放开**：UserStopExperiment 对 `Error` 态不返回"experiment is over"，而是允许触发 recover（Error 态故障可能驻留，正是要清的）。
3. **单测**：webhook `ValidateUpdateController_*` 纯函数测覆盖"running→recover 放行 / running→inject 拒 / error→recover 放行 / success→pause 路径"。

### 9.2 【修订·亲证】G1 start/stop 的 uuid 语义不同——v3 原文"都用 experimentUUID"是错的

**亲证**：
- `experiment.go:126-143` StartExperiment：`:uuid` → `GetExperimentByUUID(uuid)` → `StartExperiment(uuid,...)` ＝ **实验 experimentUUID**。
- `experiment.go:153-160` StopExperiment：`:uuid` → `UserStopExperiment(experimentInstanceID)` → `GetExperimentInstanceByUUID` ＝ **实例 instanceUUID**。
- 现存正UI作的停止 `stopExperimentResult({uuid: resultDetail.uuid})`（`ExperimentResultDetail/index.tsx:125`）传的就是**实例 uuid**，碰巧用对了。

**修订**：`useExperimentAction` 必须按动作区分 uuid：start 用 `experimentUUID`、stop/pause/resume 用 `experimentInstanceUUID`（pause/resume 新路由也走实例 uuid，与 stop 对齐）。`ExperimentRunPanel` props 两个 uuid 都要解构并分别下传。**禁止**改成统一 experimentUUID——那会破坏现存可工作的停止。

### 9.3 【修订·亲证】G1 stop 按钮 disabled={terminal} 把 Error 态停死了——与设计自相矛盾

**亲证**：`ExperimentRunPanel.tsx:117` `disabled={terminal}`，且 `constants/index.ts:310-315` `TERMINAL_RUN_STATES=['Stopped','Succeeded','Failed','Error']` → Error 是终态 → stop 被禁。但设计明确"Error 态故障可能仍驻留，必须能 exec stop 收尾"。

**修订**：TERMINAL Run states 里 **Error 不应阻碍 stop**。改 `ExperimentRunPanel` 的 stop disabled 逻辑为 `disabled={normalized === 'Stopped' || normalized === 'Succeeded'}`（已完成恢复的终态才禁），允许从 `Failed/Error/Paused/Running/Recovering` 任意态 stop。配合 G0 后端放开 + routine 不再拒 Error，形成"任意非终态可 stop 回 initial"的完整闭环。

### 9.4 【新增·codex 发现】G2 pause/resume 丢失剩余超时——orphan_pid+recover_deadline 不足

codex 指出：v2 加了 `orphan_pid`/`recover_deadline` 列，但 pause 时 kill 孤儿 timer + resume 时重启 timer，**剩余时长 = recover_deadline - now** 必须持久化且能精确恢复；现有两列只够"判断是否该 recover"，不够"resume 时按剩余时长重建 timer"。

**修订 G2**：pause handler 需额外持久化 `paused_at`（pause 时刻）；resume 时 `新 deadline = now + (recover_deadline - paused_at)`。或更简：pause 不真 kill timer，而是 pause = `orphan_pid` 进程发 SIGSTOP（连孤儿 `sleep` 进程一起停），resume 发 SIGCONT——剩余时长由孤儿进程自身 `sleep N` 自然保留，**零额外持久化**。优先选后者（更小爆炸半径、复用 SIGSTOP 原语）。需核对 chaosmetad 孤儿进程模型是否支持对其 SIGSTOP/SIGCONT（`cmdexec.StartSleepRecover` fork 的 `bash -c 'sleep N; recover'` 可被信号控制）。

### 9.5 【修订·codex 发现】G3 metrics 延迟算法错

codex 指出 P2：v3 原文"latency = update_time - create_time"错——`update_time` 会被孤儿 timer/recover 覆盖。

**修订 G3**：chaosmetad storage 不足支撑延迟统计。务实方案：
- chaosmetad 新增 `inject_duration_ms` 字段：在 `ProcessInject` 成功时记录"inject 调用耗时"（从 inject 开始到 status=success 写入的墙钟），这是真延迟。recover 耗时同理记 `recover_duration_ms`。
- metrics 端点从这两字段算 p50/p90/p99/max，跨节点由 platform 合并（无单节点多样本时直接取该节点单值，不做虚假分位数聚合——codex P2 第二条）。
- 单测覆盖：给定 N 条记录的 inject_duration_ms，分位数计算正确。

### 9.6 codex 结论与本人裁定

codex 提示它还在自查"stop disabled for Error"——本人已亲证此条属实（见 §9.3），无需等其复查。codex 的 4 条 P1 + 2 条 P2 **全部接受**，v3.1 已纳入。原 §2.2.2"统一用 experimentUUID"等错误句子以本节 §9.2 为准覆盖。

**go/no-go**：方案在补 G0 后 go。**G0 是全部前置阻塞**——不补则 G1/G2/G3 任何接线都是空架子，无法满足"任意态停止回 initial"的硬验收。编码按 §8 修订后的顺序：**G0 先行**。

### 9.7 膨胀半径再核（G0 影响面）

G0 改 webhook `ValidateUpdate` 与 platform `UserStopExperiment`——都属故障注入 CR 链路，不动 flow/measure operator。webhook 放开是"增加允许的更新路径"而非减弱既有约束（仍是 targetPhase-only、仍是 inject→recover/pause 单向），**不放宽到允许改 Spec.Experiment/Selector**，守住"注入配置不可中途改"的安全边界。

---

## 10. 第 2 次 codex 审查反馈 → v3.2 修订（2026-07-18）

codex-reviewer-2 读真实代码独立核实后抓到一条关键缺陷，本人亲证属实：

### 10.1 【P1·已修】pause 路径"接受但未执行"——空壳

- **发现**：v3.1 在 webhook 放开了 `running→pause`（TargetPhase=PausePhaseType），但 `solveFinalStatus`（`handler.go:263`）`if TargetPhase==Phase || TargetPhase != RecoverPhaseType { return }` —— 只对 recover 切换，pause 被无声忽略。且全 `phasehandler/` grep pause handler **零结果**。结果：pause 被 webhook 接受、前端按钮能点、但 operator 收到 `TargetPhase=Pause` 后什么也不做，注入继续跑。
- **亲证**：`grep -rn "PausePhase|SolvePause" phasehandler/` → 空。`handler.go:263` 确实只切 recover。codex 发现属实。
- **决策**（务实诚实，不造假实现）：webhook **回退 pause 放开**，只保留 stop（→recover）的放开——这是 codex 第一/二次都确认的真需求且 G0+G2 有真闭环（任意态 stop → recover → clean terminal，已被 10 单测守护）。pause/resume 真原语（design §9.4 的 SIGSTOP 孤儿 timer）需 chaosmetad 进程级改动 + envtest 验证，本环境无法验证，**按"无验证手段不造假实现"原则不在本期上空壳**。
- **落地**：
  - `experiment_webhook.go`：running 态 `if TargetPhase==Recover → allow; else → reject "pause not yet supported"`。测试 `running->pause allowed` 改为 `running->pause rejected (no pause handler yet)`。
  - `useExperimentAction.ts`：pause/resume 前置给 `notification.warning("暂未启用，底层 operator 暂未落地 pause-phase-handler")`，不发请求不假装成功。
  - 前端 Paused 状态枚举、状态机时间线保留（为未来 pause 落地留接口，不影响当前）。
- 10/10 单测 PASS、operator 交叉编译绿、前端 build 绿。

### 10.2 codex-review-2 其他核实项（无问题）

codex 同时核实了 G0 安全边界（只 targetPhase 变更/禁改配置/单向）、G1 双 uuid、G3 latency 诚实降级、爆炸半径——均与实现一致。唯一可修项即 §10.1，已修。

### 10.3 go/no-go

两次 codex 审查：(1) 方案 4P1+2P2 全采纳→v3.1；(2) 代码 1个P1 已修→v3.2。**无遗留重大问题，go 进入推送阶段**。诚实边界：pause/resume 真原语、K8s/Argo 6 场景、CRD yaml 重生成、7×24h RSS 仍需集群验证，已在测试报告标注。
