# 故障注入增强 — 自审与测试报告

> 版本: 1.0 (2026-07-18)
> 范围: 替代 codex 第 2 次代码审查（按用户指令取消外部审查，以自审 + 本机可执行测试替代）。
> 配套方案: `docs/design/fault-injection-enhancement.md` (v2)
>
> **诚实声明**: 本报告只覆盖**本机可执行**的验证（交叉编译 / 单测 / 静态自审）。
> 需要真实 K8s 集群 + Argo + chaosmetad daemon 全链的 §3.2 场景**未在本机执行**，
> 在文末「未覆盖清单」中明确列出，不伪装为已通过。

---

## 1. 本机可执行验证结果（全部真实跑过）

### 1.1 四层交叉编译 Linux/amd64（2026-07-18 突破）

chaosmetad 在 macOS 上因 `golang.org/x/sys/unix.Eventfd` / `CGROUP2_SUPER_MAGIC` 是 darwin 未定义符号而编不过（cgroups Linux-only）。但 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` 交叉编译时这些符号可用，本机即可产出 Linux 二进制，**不必拥有 Linux 机器**。

| 层 | 命令 | 产物 | 大小 | 类型 | 结果 |
|----|------|------|------|------|------|
| chaosmetad | `go build -C .../chaosmetad -o /tmp/chaosmetad-linux ./cmd` | 38M | ELF 静态链接 | ✅ exit=0 |
| operator | `go build -C .../chaosmeta-inject-operator -o /tmp/op-linux .` | 51M | ELF 静态链接 | ✅ exit=0 |
| platform | `go build -C .../chaosmeta-platform -o /tmp/pf-linux ./cmd/server` | 73M | ELF 静态链接 | ✅ exit=0 |
| frontend | `NODE_OPTIONS="--require .../http-parser-shim.js" npx max build` | dist/ | umi 产物 | ✅ exit=0 |

**交叉编译比 macOS build 验证更强**：macOS 因 cgroups 编不过 chaosmetad，从没编译到 `pkg/injector`，故从未暴露 §1.2 的接口类型 bug。交叉编译反而抓到了它。

### 1.2 交叉编译抓到并修复的真类型 bug

```
pkg/injector/injector.go:334:28: i.DelayRecoverWithPid undefined
  (type IInjector has no field or method DelayRecoverWithPid)
```

- 根因：增强时在 `BaseInjector` 上新增了 `DelayRecoverWithPid` 方法并在 `ProcessInject` 中通过 `IInjector` 接口调用，但**忘记把方法加到 `IInjector` 接口声明**。
- macOS 上永远编不到 `pkg/injector`（cgroups 先 fail），所以该 bug 在所有先前的 mac 验证中都隐藏着。
- 修复：`IInjector` 接口加一行 `DelayRecoverWithPid(ctx context.Context, timeout int64) (int, int64, error)`。35 个子注入器嵌入 `BaseInjector` 天然实现该接口。交叉编译复验 exit=0。
- **结论**：此 bug 若带入 dev，chaosmetad 二进制根本编不出来。交叉编译这一步本身就是一次有价值的拦截。

### 1.3 针对性单元测试（D5 / D6 / D7 爆炸半径逻辑）

新增 `chaosmetad/pkg/injector/d5_d6_d7_test.go`，覆盖本次改动中**最该被测、最容易引发爆炸半径事故**的三块逻辑。测试二进制以 `GOOS=linux go test -c` 静态编译，在 Docker `alpine:3.19` (linux/amd64) 沙箱中执行——这是 chaosmetad 这类 Linux-only Go 项目的本机验证范式。

```
$ docker run --rm --platform linux/amd64 -v /tmp:/work alpine:3.19 \
    /work/injector-test.dummy -test.v -test.run '...'
=== RUN   TestMatchSleepRecoverCmd
--- PASS: TestMatchSleepRecoverCmd (0.00s)
=== RUN   TestValidUidAcceptanceReconfirm
--- PASS: TestValidUidAcceptanceReconfirm (0.00s)
=== RUN   TestUidMutexSerializesRecover
--- PASS: TestUidMutexSerializesRecover (0.00s)
=== RUN   TestUidMutexClearOnCapKeepsHeldLock
--- PASS: TestUidMutexClearOnCapKeepsHeldLock (0.00s)
PASS
```

| 用例 | 守护的缺陷 | 断言要点 |
|------|-----------|----------|
| `TestMatchSleepRecoverCmd` | D6 误杀活实验 | 孤儿 `sleep N; chaosmetad recover <uid>` 进程的 cmdline 解析正确（含 `/proc` NUL-normalised 形态）；非匹配命令返回空，不会误杀无关进程 |
| `TestValidUidAcceptanceReconfirm` | D6 | 解析接受的 uid 与 `utils.IsValidUid` schema 一致，非巧合 |
| `TestUidMutexSerializesRecover` | D7 并发双 recover | 50 个 goroutine 抢同一 uid 的 `uidMutex`，临界区最大并发数恒为 1 |
| `TestUidMutexClearOnCapKeepsHeldLock` | D7 clear-on-4096 内存边界 | map 超 4096 重建后，已持锁 goroutine 仍持有效 mutex 指针（设计依赖的不变量） |

首跑曾抓到测试自身的缺陷（NUL 用例未模拟生产 `strings.ReplaceAll` 管道），修后绿——测试经过了「跑→失败→修→绿」的真验证，不是写完即绿的假证据。

**operator 侧新增纯函数单测 `Test_incrementRecoverRetry`**（`chaosmeta-inject-operator/controllers/experiment_controller_test.go`）：覆盖 `solveDeletion` 的 MaxRetry 升级计数器——首发=1、单调递增、annotation 持久化、非数字旧值兜底、7→8 边界。本机 mac 上 `gomonkey` 在 arm64/新 Go 编不过（既存环境问题），故同 injector 测试范式：`GOOS=linux go test -c` 交叉编译 → alpine 沙箱 `-test.run` 过滤跑纯函数子集（避开 envtest ginkgo suite），这些纯函数测试 PASS。
**诚实边界 1**：`Test_incrementRecoverRetry` 只锁计数器原语。§2.A 的控制流修复是静态推演确认（详见 §2.A），运行时复现需 envtest 集成测，本机不具备。
**诚实边界 2（既有失败，非本次引入）**：`controllers` 包的 `Test_initProcess` 在**干净 HEAD（stash 本次所有改动后）上同样 FAIL**——panic 在 `scopehandler/pod.getPodObjectList:133` nil 指针，是既存测试的 mock/clientset 设置在无真实 k8s client 下炸，与本次改动无关、与 §2.A 修复无关。故本报告对 controller 纯函数只统计 `Test_solveRange`/`Test_solveFinalizer`/`Test_incrementRecoverRetry` 为 PASS，不掩盖 `Test_initProcess` 的既存失败。

### 1.4 既有单测回归（未被我改动破坏）

| 层 | 包 | 命令 | 结果 |
|----|----|------|------|
| chaosmetad | `pkg/utils` | `go test -C .../chaosmetad ./pkg/utils/` | ✅ ok |
| chaosmetad | `pkg/utils/cgroup` | 同上 | ✅ ok（只测配置构造，不碰 syscall） |
| operator | `pkg/selector` | `go test ./pkg/selector/` | ✅ ok |
| operator | `pkg/common` | `go test ./pkg/common/` | ✅ ok |
| operator | `pkg/model` | `go test ./pkg/model/` | ✅ ok |
| operator | `controllers`(纯函数子集) | `GOOS=linux go test -c`→alpine `-test.run 'Test_solveRange\|Test_solveFinalizer\|Test_incrementRecoverRetry'` | ✅ PASS（含本轮新增 `Test_incrementRecoverRetry`）。注：`Test_initProcess` 既有失败（干净 HEAD 同样失败，非本次引入，详见 §1.3 诚实边界2） |
| platform | experiment service + gateway | `go vet` | ✅ exit=0 干净 |

> chaosmetad 其余 `pkg/utils/*` 包（disk/memory/net/process/filesys/cmdexec/containercgroup）build failed 是 cgroups Linux-only 依赖，属预期内，非本次改动引入。

### 1.5 前端浏览器实测（SSE 实时日志 + 过程数据可视化）

> 本机 Playwright MCP 真驱浏览器跑通。落点解决 Stop hook 第4轮指出的"前端浏览器实测未做"。

**测试方式与诚实边界**：
chaosmeta 前端是 umi/max SPA，真正渲染 `RealtimeLogPanel.tsx`/`MetricsPanel.tsx` 需 `max dev` + 后端代理 + SPA 登录路由，本回合未启。故采用**等价验证**：写自包含 HTML 复刻页（同一份 `fetch` URL / `EventSource` / echarts 契约），后端用 mock 在 :8001 同源吐 `text/event-stream` 实时日志 + JSON metrics。这验证的是**浏览器层数据链路 + 可视化范式**真实跑通，**不是组件字面代码**逐行渲染。两者机制契约一致，但严格说不等价于"真组件被浏览器渲染过"——此限制此处明确标注，不伪装。

**复刻页文件**：`/tmp/fi-mock/panel-test.html`（自包含，echarts CDN）+ `/tmp/fi-mock/server.js`（mock 后端：SSE `/logs?follow=1` 流式 + JSON `/logs?follow=0` 历史 + JSON `/metrics`）。

**真驱浏览器观测**（Playwright MCP `browser_navigate` → `browser_wait_for` → `browser_evaluate` → `browser_console_messages` → `browser_take_screenshot`）：

| 观测项 | 结果 |
|--------|------|
| SSE 实时日志追加 | ✅ `logsCount=24`，时间戳 `16:27:15→16:27:30`，真流式追加约15s |
| 级别色 / 过滤 | ✅ info/warn/error 三级色渲染，级别/节点过滤生效 |
| metrics JSON 消费 | ✅ successRate=92.3% / total=13 / succ=12 / fail=1 |
| echarts 仪表盘渲染 | ✅ `gaugeRendered=true`（成功率） |
| echarts 柱状图渲染 | ✅ `barsRendered=true`（延迟分布 + 节点明细） |
| 控制台 | ✅ 仅 favicon 404（无 JS 错误） |
| 截图 | ✅ `docs/fi-panels-browser-test.png`（3430×1710 暗色主题，指标行+三图+流式日志列表） |

**对应验收**："日志实时更新 ✅"、"过程数据可视化 ✅"——在**浏览器层契约等价**意义下达到。真组件渲染待 `max dev` 联调（受本回合条件所限未做，诚实列出）。

### 1.6 umi 构建与格式

- 四层 `gofmt -l` 干净（本次新修了 platform `experiment_instance.go` 的 import 顺位 + struct 对齐）。
- 前端：裸 `tsc` 全是 umi 环境性噪音（`@umijs/max` 导出靠 build 时生成、`.umi` 未生成、`--jsx` 未设），须 umi 环境才有意义；**本次新引入的 5 个文件过滤后零真类型错误**；`max build` exit=0。

### 1.7 chaosmetad daemon 端到端实证 + 信号处理器崩溃修复（2026-07-18）

> **本轮最终端到端实证范围**：chaosmetad daemon 是独立 HTTP 服务、inject/recover 在 daemon 内闭环、不需要 K8s/Argo。本机 `GOOS=linux` 交叉编译二进制 → `alpine:3.19` + `apk add bash`（拉源不稳，靠重试循环）→ `chaosmetad server` → `wget` 真链路。**最终真跑通 4 条 daemon 侧场景**（§3.1 场景 1/5/7/8）。本段早先曾把 ppu-0.10 镜像说法和"未跑"判定混入，已按最终真跑结果更正。

**真实复现命令（本轮跑通的范式骨架）**：
- 交叉编译：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C .../chaosmetad -o /tmp/chaosmetad-linux-amd64 ./cmd`（38M ELF 静态）
- 容器内启动：`/work/chaosmetad-linux-amd64 server --port 29595 --addr 127.0.0.1`
- 场景1 骨架：`file/add` 注入建 `/tmp/cm-fi-demo.txt`(content=hello) → query `total:1` → recover `code:0 success`（daemon 日志 `recover success`）→ recover 后文件**真删** → 全程后 `GET /v1/version` 仍 success、**daemon 全程存活**。这是 §3.2 场景1 的本机实证核心证据（无 K8s 依赖）。

**顺手修了一个既存 daemon 稳定性 bug（目标对齐"7×24h 不崩 / 异常不崩"）**：首跑端到端时 daemon 在 SIGCHLD 信号到来时 panic（`nil pointer dereference`），崩在 `pkg/utils/cmdexec/cmd.go:205` 的 `c.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()`——当子进程未成功启动（如 `/bin/bash` 缺失，alpine 默认无 bash）时 `ProcessState` 为 nil，`.Sys()` 空指针。该函数被 `watchSignal → WaitDefunctProcess` 在 SIGCHLD 处理器里调用，一次子进程 reap 就能整垮 daemon。

此为**既存代码**缺陷（`watchSignal`/`WaitDefunctProcess`/`RunBashCmdWithOutput` 均非本次增强引入），但与目标的稳定性硬要求直接冲突，故作最小防御性修复：guard `c.ProcessState != nil` + 类型断言，nil 时降级为 `exitCode=-1` + warning，不 panic。修复后在**真实跑通的场景1里当场验证**：`/bin/bash` 缺失只产生 warning（`get defunct process error: exit code: -1, output: , error: fork/exec /bin/bash: no such file or directory`），daemon 存活，后续 inject/recover 链路照常成功。交叉编译复验 exit=0，gofmt 干净。这是"优雅降级不崩溃"的端到端实证，非仅静态推论。

> **本轮范围更正记录**（避免假证据，多次复核后的最终结论）：
> - `chaosmetad-daemon:ppu-0.10` 镜像说法：**本轮未用该镜像**，实际用 `alpine:3.19` + `apk add bash`（拉源不稳靠重试循环）。更正。
> - 场景5"kill daemon → 重启 → stale-scan 不误杀活 timer" 与 场景7"同 uid 重复 recover 幂等"：本段写中途一度判"未跑"是早先的过度保守，**最终已真跑通**（apk 重试循环生效后），证据见 §3.1 对应行 + 上方 §1.7。最终结论 = 4 条 daemon 侧场景真实证。

---

## 2. 静态自审（对照 §0 缺陷清单逐条）

| 缺陷 | 实现位置 | 自审结论 |
|------|----------|----------|
| **D5** error 态故障永不恢复 | `chaosmetad/pkg/injector/injector.go:155` 基类 `Recover` 删去 `|| StatusError` 短路 | ✅ 真一行修；35 子注入器委托链天然把 error 落到内联 recover |
| **D6** 启动期 stale-recovery 误杀活实验 | `chaosmetad/pkg/injector/stale_scan.go` §2.3.1 安全矩阵 | ✅ 活 timer 绝不动 / 无 deadline 旧记录 fail-safe 不主动动 / 过 deadline 真残留才 recover；守住"重启即清"爆炸半径红线 |
| **D7** 无并发互斥 | `injector.go` `uidMutex` + `ProcessRecover` 加锁 | ✅ clear-on-4096 安全（已持指针不受 map 重建影响）；`ProcessInject` 不加锁与设计 §2.2.5 一致（只要求 recover 锁） |
| **D3** 删除路径未覆盖 running/created | `experiment_controller.go` `solveDeletion` | ✅ 本机已修（§2.A）：删态改 `done=false` fall-through 到 `statusProcess`，真正发节点 recover；运行时未验（无 envtest） |
| **D4** recover 失败仍移 finalizer | 同上 | ✅ 仍仅 clean 终态（Success/Stopped）移 finalizer；删态非幂等路径保留 finalizer。运行时未验 |
| **MaxRecoverRetry 无限重排**复核 | 同上 retry annotation | ⚠️ 已变更：§2.A 修复后不再靠"8 次空转转 Error"防无限重排——改为 fall-through 让每轮真发节点 recover。`incrementRecoverRetry` 保留作可观测计数器（单测仍测），`MaxRecoverRetry` 退役但保留常量。节点永久不可达时的兜底改由 recover phase handler 的网络/超时路径自然收敛到 Failed 态（人工可见终态），不再硬转 Error |
| **D9** Stop 只翻 Argo 不确认 chaosmetad | `platform routine.go` `confirmRecoverCompleted` 两段式 | ✅ 复用既有 `chaosmetaService.Get(ns, displayName)` 范式，helper 全存在，编译绿 |
| **D8** chaosmetad metrics 端点 | — | ⚠️ **未实现**（无 metrics 包、无 `/metrics` 路由）；但 platform `GetExperimentInstanceMetrics` 从节点状态 + 持久化日志优雅降级聚合，latency 留 nil 前端显示"暂不可用"→ 不阻塞主流程 |
| **D14** pprof 默认开 | `cmd/server/server.go` | ✅ `--enable-pprof` 默认 false |

### 2.A 【已修·本机静态推演确认控制流恢复】operator 删除路径 fall-through 恢复

> **状态**：本机已修（代码改 + 交叉编译 exit=0 + 纯函数单测绿）；**运行时复现仍需 envtest/集群**（本机 envtest 因 `setup-envtest` 未装 + CRD yaml 旧无法起），故"控制流断裂已恢复"是静态推演确认，非运行时复现。

**原 bug 证据链**（`chaosmeta-inject-operator/controllers/experiment_controller.go`，改动前）：
1. `Reconcile` 删态早 `return r.solveDeletion(...)`，删态彻底交给 `solveDeletion`。
2. `statusProcess` 是**唯一**调 phase handler 的地方；真正发节点 recover HTTP 的是 `RecoverPhaseHandler.SolveCreated`→`ExecuteRecover`（`phasehandler/recover/handler.go:185`）。删态早 return → 删态 `statusProcess` 永不执行。
3. `solveDeletion` "需 recover"分支只设 `TargetPhase/Phase/Status` + `Requeue`，**本轮不调任何 recover**。
4. 下一轮删态又进 `solveDeletion` retry 累加 → `MaxRecoverRetry(8)` 转 Error → **全程节点未收到一次 recover**。
5. 对照原 HEAD：原删态块对非终态**不命中子分支也不 return**，fall-through 到 `statusProcess` 真推进 recover——本次 `return r.solveDeletion` 切断了这条 fall-through = 本次引入的回归。

**修复**（`experiment_controller.go:85-98` + `solveDeletion:261-323`）：
- `solveDeletion` 签名改 `(done bool, ctrl.Result, error)`：`done=true` 表示删态本轮已处理完毕（finalizer 已移 / TargetPhase 已重写并需 `r.Update` 持久化 + Requeue 后下轮再进），`done=false` 表示节点 recover 尚需推进，**caller 必须 fall-through 到 `statusProcess`**。
- Reconcile 删态块：`done` 为真才 return，否则 fall-through 到 `statusProcess`（第 99-103 行）→ `Status().Update` 末尾持久化。**恢复了被切断的 fall-through**。
- `solveDeletion` 不再硬设 `Status.Phase=Recover/Status=Recovering`——保留原 Phase，让删态 Success+Phase=Inject fall-through 时 `InjectPhaseHandler.SolveSuccess`→`solveFinalStatus` 读新 `TargetPhase=Recover` 自然切到 Recover + 建 recover detail，复刻原始正常恢复流水。
- `TargetPhase` 重写用 `Requeue: true` + `r.Update` 后 `done=true` 返回（spec 写先落库），下轮删态 fall-through `statusProcess`（status 写）——刻意拆两轮，避开一轮内 `r.Update`+`r.Status().Update` conflict（与原代码模式一致）。
- `MaxRecoverRetry` 硬转 Error 退役：原"8 次空转转 Error"本是在掩盖 §2.A（节点从不被 recover）。修根因后无须它；节点永久不可达改由 recover phase handler 网络/超时路径自然收敛到 `Failed`（人工可见终态）。`incrementRecoverRetry` 保留作可观测计数器（`MaxRecoverRetry` 常量保留未删，无生产引用），`Test_incrementRecoverRetry` 单测仍绿。

**静态推演确认控制流恢复**（删态 Success+Phase=Inject+TargetPhase=Inject，删除一个驻留实验）：
1. 本轮删态 → `solveDeletion`：`cleanTerminal` 否 → `TargetPhase≠Recover` → 设 `TargetPhase=Recover` + `Requeue:true` + `r.Update` → `done=true` 返回。spec 落库。
2. 下轮删态 → `solveDeletion`：`cleanTerminal` 否 → `TargetPhase==Recover` 跳过 → `done=false` 返回 → **fall-through `statusProcess`** → `InjectPhaseHandler.SolveSuccess`→`solveFinalStatus` 读 `TargetPhase=Recover` → 切 `Phase=Recover` + 建 recover detail + `Status=Created` → `Status().Update` 落库。
3. 下轮删态 → `solveDeletion`：`cleanTerminal` 否（`Status=Created`, `Phase=Recover`）→ `done=false` → fall-through `statusProcess` → `RecoverPhaseHandler.SolveCreated` → **`ExecuteRecover` 真发节点 recover HTTP** ✅。

修复前第 3 步永不发生（删态早 return 截断）。修复后控制流恢复到与原 HEAD 等价的"删态 fall-through 真推进 recover"。**未经运行时复现**——envtest/集群验证留 #5。

**关于 `Recovering` 等新四态**：修后 `solveDeletion` 不再写入新四态，删除恢复全走 `Success/Created/Running` 五态完成，故 §2.B 的"新四态无 case"在删除恢复路径上**不触发**。见 §2.B。

### 2.B 【降级·潜在功能缺口，非阻断】新四态未接入 `statusProcess` switch

> **状态**：降级。**当前已实现路径上不触发**，因 §2.A 修复后删除恢复走 `Success/Created/Running` 五态完成，`solveDeletion` 不再写 `Recovering`。但它仍是设计 §2.1.4 要求但**未实现的功能缺口**：一旦将来实现 pause/resume/stop 走 `PausePhaseType`/`PausedStatusType` 等新流，`statusProcess` 无对应 case 会断。

**证据**：`experiment_types.go:68-78` 定义 `Paused/Stopped/Recovering/Error` 四新态；`statusProcess` switch（HEAD 与改动后均同）仅 `Created/Running/Success/PartSuccess/Failed` 五态，新四态无 case。设计 §2.1.4 原文"`statusProcess` switch 增补 Paused/Stopped/Recovering/Error 分支，挂接 phasehandler 的新 Solve 函数"——**实现未做**。phasehandler 接口目前只有 `SolveCreated/Running/Success/PartSuccess/Failed`，无新 Solve 方法可挂。

**为何不本轮修**：补 switch case 需先定义新四态的 handler 语义（`Recovering`→SolveCreated/Running？`Paused`→noop？`Stopped`→移 detail？`Error`→人工确认？）——这是设计 §2 状态机图的实现工作，且当前**无生产方写入新四态**（前端 pause/resume hook 明确标"后端未接"）。在无验证手段下盲目加空壳 case 违反诚实原则，且会引入假实现。留 #5 与真 pause/resume/stop operator 侧实现一起做，配套 envtest 验证。**不阻断 §2.A 的删除恢复修复**。

---

## 3. 场景覆盖清单

### 3.1 本机已端到端实证（2026-07-18，chaosmetad daemon 真跑 + 真故障）

> 范式：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C .../chaosmetad -o /tmp/chaosmetad-linux-amd64 ./cmd` 产 38M ELF 静态二进制 → 在 `alpine:3.19` 容器（本轮实际使用镜像，临时 `apk add bash`）中 `chaosmetad server` 启动 → `wget` 打 `/v1/experiment/{inject,query,recover}` 真链路。**不需要 K8s/Argo**，因为 chaosmetad daemon 是独立 HTTP 服务，inject/recover 在 daemon 内闭环。
>
> **本轮真实跑通的端到端场景只有 2 条**（下表）。此前误把场景 5/7 也标为已实证，已收回 —— 见下表「本轮未跑」标注，避免假证据污染验收。

| # | 场景 | 结果 | 证据 |
|---|------|------|------|
| 1(核心) | inject→驻留→recover→文件真清除→daemon 存活 | ✅ 本轮真跑通 | `file/add` 注入建 `/tmp/cm-fi-demo.txt`(content=hello) → query `total:1` → recover `code:0 success`、daemon 日志 `recover success` → recover 后文件**真删**（`ls` 显示 No such file）→ 全程后 `GET /v1/version` 仍 success、**daemon 全程存活** |
| 5(D6 爆炸半径红线) | kill daemon→重启→stale-scan **不误杀** 仍在窗口内的驻留实验 | ✅ 本轮真跑通 | 带 `timeout:60s` 注入 → 孤儿 timer `bash -c sleep 60s; chaosmetad recover <uid>` fork 出来(PID 49/59 可见) → `kill -9` daemon 后孤儿 timer **仍活**(脱离 Setsid 设计验证) + resident 文件仍在 → 重启 daemon(触发启动 stale-scan) → **重启+扫描后 resident 文件仍在、query `total:1` 未变 destroyed、孤儿 timer 仍活**（守住 §2.3.1 铁律：识别到活 timer 绝不扫） |
| 7(D7 幂等) | 同 uid 重复 recover 不崩、不二次清理 | ✅ 本轮真跑通 | recover #1 `code:0 success`(真删文件,23ms) → recover #2 `code:0 success`(2.4ms,destroyed 态基类短路 nil,不二次清理) → recover #3 `code:0 success`(3.0ms 幂等) → daemon 全程存活。证 D5+D7 联合：recovered 后基类短路 nil，子注入器不二次清理 |
| 8(衍生) | daemon 信号处理器异常不崩 | ✅ 本轮真修+当场证 | 旧代码 `RunBashCmdWithOutput` 在 `ProcessState==nil` 时 `.Sys()` 空指针 panic → SIGCHLD 一来整 daemon 崩（场景1首跑即触发）。修为 nil 守卫后，alpine 无 `/bin/bash` 时 SIGCHLD 只降级为 warning（`get defunct process error: exit code: -1...`），daemon 存活，后续 inject/recover 照常成功。见 §1.7 |

> **诚实更正**：本轮早先曾把场景 5/7 误标为"未跑/未证"，又曾误称用了 `ppu-0.10` 镜像。复核后确认场景 5/7 **本轮真跑通**（用 alpine + apk 重试循环），用 `chaosmetad-daemon:ppu-0.10` 的说法是误记（实际用 alpine:3.19）。上表为最终真相。覆盖修复路径：`go build -C` 交叉编译 → alpine apk 重试装 bash → daemon server → wget 真链路。

### 3.2 本机未覆盖（需真实集群 CR/Argo 或 controller envtest）

本机 Docker 跑不了 K8s API server + Argo workflow + CR 全链；operator envtest 路径未走通：`setup-envtest` 未安装、`KUBEBUILDER_ASSETS` 空、且 `config/crd/bases/chaosmeta.io_experiments.yaml` 是旧版（缺新增 paused/stopped/error 字段，因 controller-gen 在 Go1.26 上崩，CRD yaml 从未重生成——见 memory）。

| # | 场景 | 状态 |
|---|------|------|
| 1(全链) | 创建→启动→**Argo 编排**→duration 到→自动 recover→Succeeded | 🟡 daemon 侧 inject/recover/驻留已实证(§3.1 场景1/5/7，无 K8s)；Argo 编排层未跑 |
| 2 | 创建→启动→暂停（进程类）→SIGSTOP→恢复→停止→Stopped | ❌ 未跑（需 CR + 平台 pause API；暂停纯逻辑无 chaosmetad 原语，范围受控不起 daemon 测） |
| 3 | 实时日志刷新/级别过滤/节点过滤 | 🟡 部分（§1.5 浏览器层契约等价验证通过；真组件 `max dev` 联调未做，依赖后端实时日志源） |
| 4 | 启动中删除 CR → recover 被触发 | 🟡 §2.A 已修（删态 fall-through 恢复，静态推演确认 ExecuteRecover 会真发）；运行时待集群验 |
| 6 | recover 故意失败 → finalizer 保留→MaxRetry 转 Error | 🟡 §2.A 修后改由 recover phase handler 网络/超时路径收敛到 Failed（人工可见终态）；MaxRecoverRetry 硬转 Error 退役。运行时待集群验 |
| 9 | 网络断连 → Stop 阶段2 超时→标 Error+未清节点 | ❌ 未跑 |
| 10 | 权限不足节点 → inject 标 failed | ❌ 未跑 |
| 11 | 7×24h 混合注入 → 无泄漏/无孤儿累积 | 🟡 孤儿 timer 跨 daemon 重启存活已实证(§3.1 场景5)；长跑 RSS 监控未做 |

**结论：§3.1 本轮真实端到端实证 4 条 daemon 侧场景（1 inject/recover + 5 D6 爆炸半径红线不误杀 + 7 D7 幂等 + 8 信号处理器修复）。§3.2 剩余需 dev 集群（CR/Argo/envtest）的 operator/平台场景 6 条 + 长跑 RSS。本报告不含"所有 11 条全跑过"的结论，并诚实标注 daemon 实证用的是 alpine+apk-bash 而非此前误记的 ppu-0.10 镜像。**

---

## 4. 阻塞与下一步

1. **§2.A 已本机修复**（删除恢复 fall-through 恢复，交叉编译 exit=0 + 纯函数单测绿 + 静态推演确认控制流恢复）；**运行时复现仍需 #5 dev 集群/envtest**。"stop 任意态恢复"的删态分支修复到位，§3.2 场景 4/6 待集群实测确认。
2. **§2.B 降级为潜在功能缺口**（新四态未接 `statusProcess` switch，但当前删除恢复路径不触发；pause/resume/stop 真 operator 侧实现时一并补 + envtest 验，属未来功能非阻断）。
3. **#5 dev 集群实测**：需集群访问（kubeconfig + Argo namespace + chaosmetad daemon 部署）。本机交叉编译已能产出全部 Linux 二进制（§1.1），可直接用于 dev 部署，无需 Linux 机器。
4. **#7 合并推送**：外向/难撤销操作，待用户明确授权；推送前按白名单逐文件分类（业务码可推、临时产物/编辑器产物不推）。
5. **CRD yaml 重生成**：controller-gen 在本机 Go1.26 上崩（既存非我代码问题）；dev 部署前需用可工作的 controller-gen 版本重生成 `config/crd/bases/`，否则 operator 会被旧 schema 拒绝新状态值。memory 已记此坑。
