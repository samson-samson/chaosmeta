import {
  TERMINAL_RUN_STATES,
  experimentRunStatus,
  isTerminalRunStatus,
  normalizeRunStatus,
} from '@/constants';
import {
  CaretRightOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  StopOutlined,
} from '@ant-design/icons';
import { Button, Card, Space, Tooltip } from 'antd';
import MetricsPanel from './MetricsPanel';
import RealtimeLogPanel from './RealtimeLogPanel';
import RunStatusBadge from './RunStatusBadge';
import useExperimentAction from './useExperimentAction';

export interface ExperimentRunPanelProps {
  experimentUUID: string;
  experimentInstanceUUID: string;
  /** Current status string from the backend (any source). The adapter normalizes. */
  status: string | number | null | undefined;
  /** Refresh the parent's experiment status after a successful action. */
  onStatusChanged?: () => void;
  apiPrefix?: string;
}

/**
 * ExperimentRunPanel — Task 1 page assembly.
 *
 * Replaces the legacy ExperimentResultDetail layout with a clean, modern four-zone page:
 *   ① top bar: experiment status badge + action buttons (start / pause / resume / stop)
 *   ③ live log zone (RealtimeLogPanel)
 *   ④ process-data zone (MetricsPanel)
 * (Configuration card ② is rendered by the parent for create/edit; this panel is the run view.)
 *
 * Interaction feedback (Task 1 requirement):
 *   - every action shows loading (disabled + spin), success toast, or error notification (D13)
 *   - pause is disabled unless the run is in a pauseable state (Running only — kernel/docs attached
 *     faults cannot be genuinely paused, see design §2.1.1)
 *   - stop is always available from any non-terminal state (the whole point: stop must recover from
 *     any state including error, back to a clean initial state)
 *   - start is only available from Idle / terminal states
 */
export default function ExperimentRunPanel({
  experimentInstanceUUID,
  status,
  onStatusChanged,
  apiPrefix = '/chaosmeta/api/v1',
}: ExperimentRunPanelProps) {
  const { loading, run } = useExperimentAction({ apiPrefix });
  const normalized = normalizeRunStatus(status);
  const meta = experimentRunStatus[normalized];
  const terminal = TERMINAL_RUN_STATES.includes(normalized);

  const act = async (kind: 'start' | 'stop' | 'pause' | 'resume') => {
    const ok = await run(kind, experimentInstanceUUID);
    if (ok && onStatusChanged) onStatusChanged();
  };

  return (
    <Space direction="vertical" style={{ width: '100%' }} size={16}>
      {/* ① Top bar */}
      <Card size="small" bodyStyle={{ padding: '12px 16px' }}>
        <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
          <Space size={12}>
            <span style={{ fontSize: 15, fontWeight: 600 }}>实验运行</span>
            <RunStatusBadge status={status} />
            <span style={{ color: 'rgba(0,0,0,0.45)', fontSize: 12 }}>
              {meta.labelUS}
            </span>
            {normalized === 'Error' && (
              <span style={{ color: '#ff5c5c', fontSize: 12 }}>
                异常态可能仍有故障驻留，请执行停止以清理
              </span>
            )}
          </Space>
          <Space>
            <Button
              type="primary"
              icon={<CaretRightOutlined />}
              loading={loading.start}
              disabled={normalized !== 'Idle' && !terminal}
              onClick={() => act('start')}
            >
              启动
            </Button>
            <Tooltip
              title={
                normalized === 'Running'
                  ? '暂停注入（仅进程类故障）'
                  : '仅运行中可暂停'
              }
            >
              <Button
                icon={<PauseCircleOutlined />}
                loading={loading.pause}
                disabled={normalized !== 'Running'}
                onClick={() => act('pause')}
              >
                暂停
              </Button>
            </Tooltip>
            <Button
              icon={<PlayCircleOutlined />}
              loading={loading.resume}
              disabled={normalized !== 'Paused'}
              onClick={() => act('resume')}
            >
              恢复
            </Button>
            <Button
              danger
              icon={<StopOutlined />}
              loading={loading.stop}
              disabled={terminal}
              onClick={() => act('stop')}
            >
              停止
            </Button>
          </Space>
        </Space>
      </Card>

      {/* ③ Real-time log */}
      <Card size="small" title="实时日志" bodyStyle={{ padding: 16 }}>
        <RealtimeLogPanel
          experimentInstanceUUID={experimentInstanceUUID}
          apiPrefix={apiPrefix}
        />
      </Card>

      {/* ④ Process data */}
      <Card size="small" title="过程数据" bodyStyle={{ padding: 16 }}>
        <MetricsPanel
          experimentInstanceUUID={experimentInstanceUUID}
          apiPrefix={apiPrefix}
        />
      </Card>
    </Space>
  );
}

// re-export for parent composition
export { isTerminalRunStatus };
