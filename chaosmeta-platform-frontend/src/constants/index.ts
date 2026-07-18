export const DEFAULT_NAME = 'Umi Max';

// KubernetesController使用
// -1: 开发环境，0：生产环境
export const envType = 0;

export const tagColors = [
  {
    color: '#EDEEEF',
    type: 'default',
    borderColor: '#DADADA',
  },
  {
    color: '#FFD7D7',
    type: 'red',
    borderColor: '#F8B4B4',
  },
  {
    color: '#FFF2B5',
    type: 'yellow',
    borderColor: '#FFE361',
  },
  {
    color: '#CDCCFF',
    type: 'purple',
    borderColor: '#B2B1FF',
  },
  {
    color: '#FFE0CB',
    type: 'orange',
    borderColor: '#FFCDAA',
  },
  {
    color: '#DAFFA7',
    type: 'green',
    borderColor: '#C5FF71',
  },
];

// 编排节点类型对应颜色
export const arrangeNodeTypeColors: any = {
  fault: '#F5E2CC',
  measure: '#C6F8E0',
  flow: '#FFD5D5',
  other: '#D4E3F1',
};

export const nodeMode = {
  fault: '故障节点',
};

// 计算会有小数问题，直接在这里列举处理了
// secondStep 每段时间轴的间隔时间
// 宽度对应时间，默认1s为3px
// widthSecond 1s对应的宽度
export const scaleStepMap: any = {
  33: {
    secondStep: 90,
    widthSecond: 1,
  },
  66: {
    secondStep: 45,
    widthSecond: 2,
  },
  100: {
    secondStep: 30,
    widthSecond: 3,
  },
  150: {
    secondStep: 20,
    widthSecond: 4.5,
  },
  200: {
    secondStep: 15,
    widthSecond: 6,
  },
  300: {
    secondStep: 10,
    widthSecond: 9,
  },
};

// 触发方式选项
export const triggerTypes = [
  { label: '手动触发', value: 'manual', labelUS: 'manual trigger' },
  { label: '单次定时', value: 'once', labelUS: 'single timing' },
  { label: '周期性', value: 'cron', labelUS: 'periodicity' },
];

// 实验结果状态
export const experimentResultStatus = [
  {
    value: 'Pending',
    label: '等待中',
    labelUS: 'pending',
    color: 'blue',
    type: 'info',
  },
  {
    value: 'Running',
    labelUS: 'running',
    label: '运行中',
    color: 'blue',
    type: 'info',
  },
  {
    value: 'Succeeded',
    labelUS: 'succeeded',
    label: '成功',
    color: 'green',
    type: 'success',
  },

  {
    value: 'Failed',
    labelUS: 'failed',
    label: '失败',
    color: 'red',
    type: 'error',
  },
  {
    value: 'error',
    labelUS: 'error',
    label: '错误',
    color: 'red',
    type: 'error',
  },
];

// 实验状态
export const experimentStatus = [
  {
    value: 0,
    label: '待执行',
    labelUS: 'to be executed',
    color: 'blue',
  },
  {
    value: 1,
    label: '执行成功',
    labelUS: 'execution succeed',
    color: 'green',
  },
  {
    value: 2,
    label: '执行失败',
    labelUS: 'execution failed',
    color: 'red',
  },
  {
    value: 3,
    label: '执行中',
    labelUS: 'executing',
    color: 'blue',
  },
];

/* ============================================================
 * Task 1 (D10 fix): unified experiment run-state model.
 *
 * The previous codebase exposed two incompatible enums — experimentStatus (numeric 0-3) and
 * experimentResultStatus (string Pending/Running/Succeeded/Failed/error) — and neither could
 * represent Paused / Stopped / Error. The unified 8-state model below is the single source of
 * truth for the page UI. The adapter maps every legacy multi-source status (CRD StatusType,
 * chaosmetad 4-state, Argo workflow phase, the two legacy enums) into these 8 states so all
 * consumers can switch on one coherent set.
 * ============================================================ */

export type ExperimentRunStatus =
  | 'Idle'
  | 'Running'
  | 'Paused'
  | 'Stopped'
  | 'Recovering'
  | 'Succeeded'
  | 'Failed'
  | 'Error';

export const experimentRunStatus: Record<
  ExperimentRunStatus,
  {
    value: ExperimentRunStatus;
    label: string;
    labelUS: string;
    color: string;
    tone: 'info' | 'processing' | 'warning' | 'default' | 'success' | 'error';
  }
> = {
  Idle: {
    value: 'Idle',
    label: '待执行',
    labelUS: 'idle',
    color: 'default',
    tone: 'default',
  },
  Running: {
    value: 'Running',
    label: '运行中',
    labelUS: 'running',
    color: 'processing',
    tone: 'processing',
  },
  Paused: {
    value: 'Paused',
    label: '已暂停',
    labelUS: 'paused',
    color: 'warning',
    tone: 'warning',
  },
  Stopped: {
    value: 'Stopped',
    label: '已停止',
    labelUS: 'stopped',
    color: 'default',
    tone: 'default',
  },
  Recovering: {
    value: 'Recovering',
    label: '恢复中',
    labelUS: 'recovering',
    color: 'processing',
    tone: 'processing',
  },
  Succeeded: {
    value: 'Succeeded',
    label: '成功',
    labelUS: 'succeeded',
    color: 'success',
    tone: 'success',
  },
  Failed: {
    value: 'Failed',
    label: '失败',
    labelUS: 'failed',
    color: 'error',
    tone: 'error',
  },
  Error: {
    value: 'Error',
    label: '异常',
    labelUS: 'error',
    color: 'error',
    tone: 'error',
  },
};

/**
 * normalizeRunStatus — the single adapter the UI uses to turn any backend / CRD / Argo status
 * string (or the legacy numeric enum) into a unified ExperimentRunStatus.
 *
 * Mapping rules (additive, backward-compatible with every existing value):
 *   - new CRD values: 'paused' → Paused, 'stopped' → Stopped, 'recovering' → Recovering, 'error' → Error
 *   - CRD created/success/failed/running/partSuccess, chaosmetad created/success/error/destroyed,
 *     Argo WorkflowPending/Running/Succeeded/Failed/Error, and the two legacy enums all map in.
 *   - unknown values fall back to Idle (safe default — never a false "running").
 */
export function normalizeRunStatus(
  raw: string | number | null | undefined,
): ExperimentRunStatus {
  if (raw === null || raw === undefined) return 'Idle';
  const s = String(raw).trim();
  // Normalize case-insensitively but keep the exact-match table authoritative.
  const key = s.toLowerCase();
  switch (key) {
    // Idle family
    case 'idle':
    case 'created':
    case 'pending':
    case '0':
    case 'to_be_executed':
    case 'tobeexecuted':
      return 'Idle';
    // Running family (includes CRD 'success'/'running' and chaosmetad 'success' = resident/running)
    case 'running':
    case 'success':
    case '3':
    case 'executing':
    case '':
      // '' is ambiguous; legacy empty status meant "not started" → treat as Idle not Running.
      return s === '' ? 'Idle' : 'Running';
    // Paused / Stopped / Recovering (new additive states)
    case 'paused':
      return 'Paused';
    case 'stopped':
    case 'destroyed':
      return 'Stopped';
    case 'recovering':
      return 'Recovering';
    // Succeeded family (terminal clean across all layers)
    case 'succeeded':
    case '1':
    case 'execution succeed':
    case 'executionsucceed':
      return 'Succeeded';
    // Failed / Error family
    case 'failed':
    case 'partsuccess':
    case '2':
    case 'execution failed':
    case 'executionfailed':
      return 'Failed';
    case 'error':
      return 'Error';
    default:
      return 'Idle';
  }
}

/** Convenience: which states are terminal (no further auto-transition expected). */
export const TERMINAL_RUN_STATES: ExperimentRunStatus[] = [
  'Stopped',
  'Succeeded',
  'Failed',
  'Error',
];

/** Convenience: which states represent a fault possibly still resident (needs explicit stop). */
export const RESIDENT_RUN_STATES: ExperimentRunStatus[] = [
  'Running',
  'Paused',
  'Error',
  'Recovering',
];

export function isTerminalRunStatus(
  raw: string | number | null | undefined,
): boolean {
  return TERMINAL_RUN_STATES.includes(normalizeRunStatus(raw));
}

// 节点类型
export const nodeTypeMap: any = {
  fault: '故障节点',
  measure: '度量引擎',
  flow: '流量注入',
  wait: '等待时长',
};

// 节点类型
export const nodeTypeMapUS: any = {
  fault: 'faulty node',
  measure: 'measurement engine',
  flow: 'flow injection',
  wait: 'waiting time',
};

export const nodeTypes = [
  {
    label: '故障节点',
    labelUS: 'faulty node',
    type: 'fault',
  },
  {
    label: '度量引擎',
    labelUS: 'measurement engine',
    type: 'measure',
  },
  {
    label: '流量注入',
    labelUS: 'flow injection',
    type: 'flow',
  },
  {
    label: '其他节点',
    labelUS: 'other nodes',
    type: 'other',
  },
];
