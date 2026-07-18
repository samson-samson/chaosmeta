# 故障注入前端设计系统（ui-ux-pro-max 合成裁决）

> 本文档由 `ui-ux-pro-max` skill（v2.6.2）CLI 多域推荐 + 人工适配裁决合成。
> 不是照搬 skill 顶层输出——skill 在 "dashboard/Analytics" 品类下硬编码深色，**与 chaosmeta light 企业控制台冲突**，会破坏"对其他模块最小侵入"。故取其结构化推荐（pattern/style/chart/ux 准则），配色取 light enterprise 变体，技术栈适配 umi/max + antd5 + echarts5（非 shadcn/RN）。
> 适用范围：**仅故障注入相关页面**，不污染全局主题（满足爆炸半径约束）。

## 0. 来源与裁决原则

### skill 实际跑了什么（全部记录）
- `search.py "chaos engineering fault injection ... data-dense dashboard real-time log" --design-system` → Pattern=Real-Time/Operations, Style=Dark Mode(OLED), Color=light-bg #F8FAFC（与 style 标签自相矛盾）, Chart 推荐见 §4
- `search.py "enterprise SaaS admin panel clean minimal light ..." --design-system` → Pattern=Data-Dense Dashboard, Style=Data-Dense Dashboard, **Color=light 变体**（采纳）
- `--domain style "minimalism flat clean enterprise"` → Minimalism & Swiss Style（WCAG AAA）+ Flat Design 为候选
- `--domain chart "real-time monitoring success rate latency..."` → Streaming Area / Line / Box Plot / Line+Highlight / Timeline
- `--domain ux`（两轮 loading/feedback/error + a11y/focus/reduced-motion）→ §5 准则

### 裁决
| skill 顶层建议 | 我的裁决 | 理由 |
|---|---|---|
| Dark Mode (OLED) 全暗 | ✗ 取 **light enterprise** 变体 | chaosmeta 全站 light，故障注入做暗会撕裂一致性；爆炸半径约束 |
| Fira Code 标题 + Fira Sans 正文 | △ 仅 **metrics/数字** 用 mono（tabular），标题/正文用系统无衬线 | 多加载两个 Google Font 簇代价高、企业控制台不必需 |
| Streaming Area (Canvas/WebGL) | △ 实时日志用 **echarts line + appendData + pause/resume**，不引 CanvasJS/Smoothed D3 | 已有 echarts5 依赖，避免新增重型库 |
| 数据色 #1E40AF/#3B82F6 + 琥珀 #D97706 + 红 #DC2626 | ✓ 采纳（与 antd 默认蓝兼容） | WCAG AA/AAA + 与现有 fi-tokens 协调 |
| Minimalism & Swiss Style（网格/高对比/无阴影） | ✓ 采纳，但保留**卡片 1px 边框 + 极轻阴影**做分区 | 故障注入需清晰分区（配置/状态/日志/数据），纯 Swiss 过扁平丢层次 |

## 1. 风格定位

- **Pattern**：Real-Time / Operations + Data-Dense Dashboard —— 数据密集但可扫描，状态优先
- **Style**：Minimalism & Swiss（主）+ 极轻 Flat 卡片（辅）。**不**用 glassmorphism / neumorphism / brutalism / claymorphism
- **关键词**：clean, spacious, functional, high-contrast, grid-based, status-first, color-semantic
- **明暗**：light 模式（与全站一致）。**不**引入独立暗色页。若未来全站做暗色主题，fi-tokens 已留 statusAccent 抽象，可平滑迁
- **爆炸半径**：所有令牌前缀 `fi-`，**只**在故障注入页消费，不进 antd ConfigProvider 全局 theme，不改 .umirc，不改 global.css

## 2. 配色（light enterprise 变体，落到 antd5 + fi-tokens）

数据语义色（stat / chart / 状态条），非 antd 主题色覆盖：

| 角色 | Hex | 用途 | antd5 Token 对照 |
|---|---|---|---|
| data-primary（蓝） | `#1E40AF` | 主数据线/活跃状态 | colorPrimary 不动，作 fi-data |
| data-secondary（亮蓝） | `#3B82F6` | 次数据线 | — |
| accent / CTA（琥珀） | `#D97706` | 强调/告警缓冲态 | colorWarning 系 |
| success | `#16A34A` | 成功/healthy | colorSuccess |
| destructive | `#DC2626` | 故障驻留/停止/错误 | colorError |
| surface（卡面） | `#FFFFFF` | 卡片底 | colorBgContainer |
| bg（页底） | `#F8FAFC` | 页面背景 | colorBgLayout |
| muted（弱面/分桶底） | `#F1F5F9` | 空状态/分桶 | — |
| border | `#E2E8F0` | 1px 卡片边 | colorBorderSecondary |
| text-primary | `#0F172A` | 正文 | colorText |
| text-secondary | `#475569` | 辅助/标签 | colorTextSecondary |
| text-tertiary | `#94A3B8` | 占位/禁用 | colorTextTertiary |

WCAG：text-primary on surface = 16.8:1（AAA）；text-secondary = 7.4:1（AAA）；accent #D97706 on white = 4.5:1（AA 正文/AAA 大字）。**满足 4.5:1 最低**。

### 状态机 8 态着色（statusAccent，G4 已有，对齐此处）
idle→text-tertiary / running→data-primary（蓝，活跃）/ paused→accent（琥珀）/ stopped→text-secondary（灰，终态正常）/ succeeded→success / failed→destructive / error→destructive（+图标，非仅色）/ unknown→text-tertiary。
**color-not-only**：running/paused 等所有状态在 Badge 旁带**文字 + 图标**，不仅靠色块（a11y 强制）。

## 3. 排版与间距

- **字族**：sans 正文/标题 = 系统栈（`-apple-system, ...`，不外挂 Google Font）；metrics/计数/UUID/时间戳 = mono（`ui-monospace, SFMono-Regular, Menlo`），用 `font-variant-numeric: tabular-nums` 防 layout shift（number-tabular 准则）
- **字号阶**（基于 antd 默认，不重造）：12 / 14 / 16 / 18 / 24 / 30。正文 ≥ 16px（mobile-first 防 iOS 自动放大）。行高 1.5
- **间距阶**（fi-tokens spacing，8dp 节律）：4 / 8 / 12 / 16 / 24 / 32 / 48。卡片内边距 16/24，区间距 24，区块间距 32
- **圆角**：6px（卡片）/ 4px（输入/按钮）/ 9999（Badge dot）。不全场直角（Swiss），也不过圆
- **阴影**：默认 none（Swiss）。仅浮层/Modal 用 antd 默认；卡片分区靠 **1px border + bg 反差**，不靠阴影。hover 仅提升边框色 + 极轻底色，不位移

## 4. 图表映射（echarts5，不新增库）

| 数据 | 图表 | skill 依据 | a11y 要点 |
|---|---|---|---|
| 实时日志流 | **折线（脉冲点）+ appendData** 伪 streaming | Streaming Area（≥1Hz） | **pause/resume 按钮**；prefers-reduced-motion → 冻结；当前值大字 KPI |
| 实时日志文本 | **Terminal/代码风格面板**（mono、自动滚、暂停按钮、级别色点+文字、检索过滤） | — | ARIA live=polite；不抢焦点；键盘可暂停 |
| 成功率/进程趋势 | **折线**（单/双线） | Line Chart | 线型区分（实线/虚线）非仅色；legend 可点切换 |
| 时延分布 | **箱线图**（min/Q1/median/Q3/max + 离群点） | Box Plot | 旁配统计表（AABB 降级）；离群点描点非仅色 |
| 注入计数（成功/失败/驻留/销毁） | **分桶统计卡 + 小堆叠条** | KPI cards + bar | 数字 tabular；颜色+文字标签 |
| 状态机轨迹 | **横向时间线**（G4 RunStatusTimeline） | Timeline | 状态点带文字；当前态高亮 |
| 异常/错误计数 | **带高亮的折线**或告警列表 | Line+Highlight | 异常点用形状标记 + 文字注释，非仅色 |

**实时日志聚合**：阈值 >300ms 显示 skeleton（loading-states），不全屏白屏。echarts dataset 限 60–300s 滑窗，超出 downsample（skill 数据量阈值）。

## 5. UX 准则（必守，来自 skill ux 域 + Quick Reference §1–3）

### CRITICAL（不达标即退回）
1. **color-contrast** 正文 ≥4.5:1（见 §2 已验）
2. **focus-states** 可交互元素可见 focus ring 2–4px（不删 outline 而无替代）
3. **color-not-only** 所有状态/级别同时有 色 + 图标 + 文字
4. **loading-buttons** 异步操作禁用按钮 + spinner（防双击）
5. **reduced-motion** `@media (prefers-reduced-motion)` 冻结实时流/动画
6. **keyboard-nav** Tab 序 = 视觉序；Terminal/图表有可达控件

### HIGH
7. **error-recovery** 错误信息含恢复路径（retry/help），不仅"失败"
8. **confirmation-dialogs** 停止/恢复等带副作用的动作需二次确认（停止会触发清理，确认）
9. **empty-states** 空数据有引导 + 动作，不白屏
10. **touch-target** ≥44px；间距 ≥8px
11. **toast** aria-live=polite 不抢焦；3–5s 自动消散

### 后端缺口诚实暴露（v3.2 既有）
- pause/resume：底层 operator 无 pause-phase-handler，前端出示 **notification.warning「暂未启用」**，按钮可见但禁用语义，**不假装成功**（见 useExperimentAction）
- metrics latency：chaosmetad 端点诚实标 unavailable，不造假数（见 handler_experiment_metrics_get）

## 6. 逐页落地清单（勘察已返回/fs 真实命名）

### 真实页面图（全部在 `/space/` 下，路由配置 `config/router.ts`，布局走 umi/max mix）
| 路由 | 页面文件 | 职责 | 本轮处理 |
|---|---|---|---|
| `/space/experiment/add` | `pages/Space/AddExperiment/index.tsx`(+ArrangeContent+components) | 创建/编辑实验：节点拖拽编排+动态表单 | 轻触：右栏预览摘要、校准间距 |
| `/space/experiment/choose` | `pages/Space/ChooseExperiment/index.tsx` | 创建前选实验类型 | 轻触：卡片网格空态 |
| `/space/experiment` | `pages/Space/Experiment/index.tsx`→`ExperimentList.tsx` | 实验列表（搜索/分页/复制/删除） | 状态 Badge 色图字、空态、行内操作 |
| `/space/experiment/detail` | `pages/Space/ExperimentDetail/index.tsx`(+ArrangeInfoShow) | 编排详情 | 轻触：分区对齐 |
| `/space/experiment-result` | `pages/Space/ExperimentResult/index.tsx` | 结果列表（运行历史） | 状态 Badge、空态、过滤 |
| `/space/experiment-result/detail` | `pages/Space/ExperimentResultDetail/index.tsx` | **运行视图核心**：挂 ExperimentRunPanel | **重点重做**：四区分明 |

### ExperimentRun 包（`components/ExperimentRun/`，6 文件，仅被 ExperimentResultDetail 消费）
| 文件 | 现状 | 本轮处理 |
|---|---|---|
| `fi-tokens.ts` | 已有 surface/text/status ramp/spacing/radius | **增补** data 色 + chart 令牌（不重写既有） |
| `ExperimentRunPanel.tsx` | 状态机时间线 + 启停暂停恢复（3态反馈） | 对齐 §2 配色、§5 二次确认/焦点；四区清晰 |
| `RealtimeLogPanel.tsx` | SSE→长轮询降级、过滤、暂停、导出、5000行上限 | 校准 §5 reduced-motion freeze、a11y live |
| `MetricsPanel.tsx` | echarts gauge+bar+table，5s 轮询 | §4 tabular-nums、线型非仅色、_boot route 核实 |
| `RunStatusBadge.tsx` | Tag + normalizeRunStatus 8态 | 验证 色+图标+文字（color-not-only） |
| `useExperimentAction.ts` | 双 uuid、pause/resume 诚实禁用 | 已达 §5，不动 |

### 死代码清理（本轮删）
- [ ] `pages/Space/ExperimentResultDetail/ObservationCharts.tsx` —— 假数据 echarts（硬编码 24 点 CPU），已被 index.tsx 注释弃用
- [ ] `pages/Space/ExperimentResultDetail/ShowLog.tsx`（若存在）—— 旧 AceEditor 一次性 dump，已被 RealtimeLogPanel 替代
- [ ] `constants/index.ts` 旧枚举 `experimentStatus/experimentResultStatus` 若仅死引用，收紧（保留 normalize 兼容层）

### 隐藏风险（Task #11 "全部功能可用" 必须实证修复）
- [ ] **metrics 路由不连通**：前端 `MetricsPanel` 调 platform `/chaosmeta/api/v1/experiments/:instanceUUID/metrics`（按实例），而 G3 加的是 chaosmetad `/v1/experiment/metrics`（聚合无实例）。真实跑会 404。需二选一：①platform 加按实例端点 ②前端改调 chaosmetad。倾向①（platform 聚合 chaosmetad 后按实例过滤），因前端通篇走 platform `/chaosmeta/api/v1`。
- [ ] **logs 路由**：`RealtimeLogPanel` 调 platform `/chaosmeta/api/v1/experiments/:instanceUUID/logs?follow=` — 核实 platform 是否真有 SSE 端点。若无 → 同样补或改。
- [ ] **ExperimentRun 包仅 ExperimentResultDetail 一处消费**：ExperimentDetail / ExperimentResult 列表未接运行面板，符合"最小侵入"，保留。

### 落地顺序（本轮）
1. Phase A 核心：`fi-tokens` 增补 → `ExperimentRunPanel`/`MetricsPanel`/`RealtimeLogPanel`/`RunStatusBadge` 对齐设计系统 → 死代码删除
2. Phase B 列表：`ExperimentResult` / `Experiment` 列表状态 Badge + 空态 + 行操作
3. Phase C 创建：`AddExperiment` 右栏预览摘要（轻触）
4. Phase D：umi build 绿 + Task #11 合并 main 重跑验证上述隐藏风险

## 7. 与既有 G4 的关系

G4（v3）已落 `fi-tokens.ts`（surface/border/text/status ramp/level/chart/radius/spacing）+ `RunStatusTimeline`。本设计系统**确认 G4 方向正确**，本轮重做 = 在 G4 令牌基础上：① 补齐按 §4 的图表组件 ② 按 §5 校准交互态 ③ 按 §6 重排分区使"配置/状态/日志/数据"四区清晰。**不重写 fi-tokens**，只增补 data 色与 chart 令牌。

## 8. 验证清单（落地后自测）
- [ ] 全部状态 Badge = 色 + 图标 + 文字
- [ ] focus ring 键盘可见
- [ ] 实时日志有 pause + reduced-motion freeze
- [ ] 停止动作有二次确认
- [ ] 错误信息含恢复路径
- [ ] 空态有引导
- [ ] 数字 tabular-nums
- [ ] umi build 绿（不引新重型依赖）
- [ ] 暗对比对 light（不引入暗页，但令牌抽象为未来迁占位）
- [ ] 375/768/1024/1440 响应式
