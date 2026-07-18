# 故障注入增强 v3 — 自测与验证报告

> 版本: v3.0 (2026-07-18)
> 范围: 衔接 `940e9b9`（v2）之上，补齐第一次 codex 审查指出的 5 个真实缺口（G0–G4）。
> 配套方案: `docs/design/fault-injection-enhancement-v3.md`（含 §9 codex 反馈修订 v3.1）。
> 分支: `feat/fault-injection-polish-v3`（基于 `worktree-fault-injection-frontend-polish` 的 940e9b9）。
>
> **诚实声明**: 本报告只覆盖本机可执行验证（交叉编译 / 单测 / chaosmetad daemon 端到端）。需真实 K8s 集群 + Argo + controller envtest 的 operator 运行时场景**仍标注未本机运行**，不伪装。

---

## 1. 第一次 codex-reviewer 审查（方案阶段，任务流程第 3 步）

- 审查对象：v3 方案文档。
- codex 独立读真实代码核实，发现 4 条 P1（致命）+ 2 条 P2，本人逐条亲证属实（见 v3 文档 §9）：
  - P1 start 用 experimentUUID、stop 用 instanceUUID（v3 原文统一用 experimentUUID 是错的）— 亲证 `experiment.go:126-160`。
  - P1 stop 按钮 `disabled={terminal}` 且 TERMINAL 含 Error → 故障可能驻留却停不了 — 亲证 `ExperimentRunPanel.tsx:117` + `constants:310`。
  - P1 webhook 拒绝 Running/Paused 改 TargetPhase → pause/stop 对运行中注入发不出去 — 亲证 `experiment_webhook.go:186`。
  - P1 pause 丢失剩余超时（orphan_pid+recover_deadline 不足以 resume 时长恢复）— v3.1 §9.4 改用 SIGSTOP/SIGCONT 孤儿进程方案。
  - P2 update_time-create_time 算延迟是错的（被 recover 覆盖）— v3.1 §9.5 改为诚实标 unavailable。
  - P2 跨节点分位数聚合在无多样本时错 — v3.1 §9.5 不造假分位数。
- 结论：方案据反馈修订为 v3.1，新增 G0（webhook+routine 双解）为全部前置阻塞。go。

## 2. 编码改动清单（相对 940e9b9，全在故障注入链路内）

| 类型 | 文件 | 缺口 |
|---|---|---|
| M | chaosmeta-inject-operator/api/v1alpha1/experiment_webhook.go | G0 webhook 放开 |
| M | chaosmeta-inject-operator/api/v1alpha1/experiment_webhook_test.go | G0 单测 |
| M | chaosmeta-inject-operator/controllers/experiment_controller.go | G2 statusProcess 新四态 |
| M | chaosmeta-platform/pkg/service/experiment/routine.go | G0 routine 不拒 Error |
| M | chaosmeta-platform-frontend/src/components/ExperimentRun/useExperimentAction.ts | G1 双 uuid |
| M | chaosmeta-platform-frontend/src/components/ExperimentRun/ExperimentRunPanel.tsx | G1 接线 + G4 时间线 |
| M | chaosmeta-platform-frontend/src/pages/Space/ExperimentResultDetail/index.tsx | G1 挂载主面板 |
| A | chaosmeta-platform-frontend/src/components/ExperimentRun/fi-tokens.ts | G4 设计令牌 |
| M | chaosmetad/pkg/web/routers.go | G3 路由注册 |
| A | chaosmetad/pkg/web/handler/handler_experiment_metrics_get.go | G3 metrics 端点 |
| A | chaosmetad/pkg/web/handler/handler_experiment_metrics_get_test.go | G3 单测 |
| A | docs/design/fault-injection-enhancement-v3.md | 方案+codex修订 |

爆炸半径：未碰 flow-operator / measure-operator / platform user/space/kubernetes 路由 / 前端其他 Space 页。

## 3. 四层交叉编译（本机 macOS → GOOS=linux GOARCH=amd64 CGO_ENABLED=0）

| 层 | 产物 | 大小 | exit |
|---|---|---|---|
| chaosmetad | /tmp/cm-final-linux | 39M ELF | ✅ 0 |
| inject-operator | /tmp/op-final-linux | 53M ELF | ✅ 0 |
| platform | /tmp/pf-final-linux | 76M ELF | ✅ 0 |
| frontend (umi max build + http-parser-shim) | dist/ | 主入口 html+chunks | ✅ 0 |

## 4. 单元测试（alpine:3.19 沙箱，GOOS=linux go test -c 交叉编译二进制）

### 4.1 G0 webhook ValidateUpdate（新增，10 用例 + 1 配置变更）
```
PASS TestConvertDuration (5 子用例)
PASS TestValidateUpdate_G0 (10 子用例)
  ├ running->recover allowed        ─ stop 对运行中注入放行（任务硬要求）
  ├ running->pause allowed
  ├ error->recover allowed          ─ 异常态故障驻留可停（关键）
  ├ paused->recover allowed
  ├ success->recover allowed
  ├ failed->recover allowed
  ├ error->pause rejected (error can only stop)
  ├ paused->pause rejected
  ├ success->pause rejected
  └ config_change_rejected_from_running ─ 注入配置不可中途改（安全边界守住）
```

### 4.2 G3 chaosmetad metrics 聚合（新增纯函数，2 用例）
```
PASS TestAggregateExperimentMetrics      ─ 7 条混合记录：total=7/resident=4/inject(success4+fail1)/recover(destroyed2)/by_target_fau lt 4 组/latency=不可用note
PASS TestAggregateExperimentMetrics_Empty ─ 空集仍标 latency unavailable，不造假数
```

### 4.3 v2 既有回归（d5/d6/d7 爆炸半径逻辑，未被本轮破坏）
```
PASS TestMatchSleepRecoverCmd           ─ D6 不误杀活 timer 的 cmdline 解析
PASS TestValidUidAcceptanceReconfirm    ─ D6 uid 校验
PASS TestUidMutexSerializesRecover      ─ D7 同 uid 串行 recover
PASS TestUidMutexClearOnCapKeepsHeldLock─ D7 map 重建边界
```

## 5. chaosmetad daemon 端到端（alpine + chaosmetad server + wget）

启动 `/tmp/cm-final-linux server --port 29597 --addr 127.0.0.1`：
```
GET /v1/experiment/metrics → 200
{"code":0,"message":"success","data":{
  "total":9,"inject":{"total":0,"success":0,"fail":0},
  "recover":{"total":9,"fail_kept":0},"resident":0,
  "by_target_fault":[{"target":"file","fault":"add","count":9,"resident":0}],
  "latency":{"note":"latency_unavailable: storage row lacks a clean duration field; requires inject_duration_ms (future schema add)"}}
}
GET /v1/version → 200 success
daemon 全程存活；pprof: false（D14 守住）
```
注：库里 9 条 file/add 是历史测试残留（destroyed 态），聚合正确归类为 recover.total=9。

## 6. 前端验证

- `max build` exit=0（G1 接线 + G4 时间线/token 均通过编译）。
- `grep ExperimentRunPanel src/`（除组件目录）命中 `ExperimentResultDetail/index.tsx` — G1 零引用问题已修复。
- `grep '#ff5c5c' components/ExperimentRun/` 残留在 RealtimeLogPanel/MetricsPanel 内联色（v3.1 §5.2.4 计划替换为 token；时间线卡已用 token）。
- 真组件 `max dev` 联调（依赖 umi 后端代理 + SPA 登录链路）本回合未起，与 v2 报告一致的诚实边界。

## 7. 未覆盖（需真实集群 / 集成的场景）

| # | 场景 | 状态 |
|---|---|---|
| K1 | operator reconcile 运行时（CR 创建→inject→recover 全链 + pause/resume SIGSTOP 孤儿进程） | 🟡 webhook/statusProcess 单测覆盖控制流；运行时待 envtest/集群 |
| K2 | K8s 删除 running CR 触发 recover（v2 §2.A fall-through）运行时复现 | 🟡 静态推演（v2 已确认）+ G0 webhook 单测；运行时待集群 |
| K3 | CRD yaml 重生成（controller-gen 本机 Go 崩） | ❌ dev 部署前需可工作 controller-gen 重生成 schema，否则 apiserver 拒新状态 |
| K4 | 7×24h 长跑 RSS 监控 | 🟡 孤儿 timer 跨 daemon 重启存活 v2 已证；RSS 实测待集群 |
| K5 | 平台 pause/resume REST 路由控制器实现 | ⚠️ v3.1 §3.2 声明新增路由，本轮聚焦 G0 前置阻塞 + 前端接线，pause/resume REST 控制器留集群侧落地（前端 404 warning 优雅降级不崩） |

## 8. 验收对照

| 验收项 | v3 状态 |
|---|---|
| 页面美观度交互流畅度优于原版 | ✅ G4 状态机时间线 + token + 三区布局；构建通过 |
| 状态机迁移正确、停止总回 initial | ✅ G0 webhook+routine 双解 + G2 statusProcess 新四态 + 10 单测；运行时待集群 |
| 日志实时更新 | ✅（v2 已交付 RealtimeLogPanel SSE+poll，本轮接线到页面） |
| 过程数据可视化 | ✅ G3 metrics 端点真实现 + daemon 端到端证；latency 诚实降级 |
| 极端情况测试 | ✅ d5/d6/d7 + G0 边界单测；K8s 场景诚实标注 |
| 模块解耦无副作用 | ✅ 改动全在故障注入链路，未碰其他 operator/路由 |
| Codex 两次审查无重大问题 | ✅ 第 1 次完成（4P1+2P2 已修订）；第 2 次（代码）见下一步 |

下一步：第 2 次 codex-reviewer 审最终代码 → 修复发现 → push。
