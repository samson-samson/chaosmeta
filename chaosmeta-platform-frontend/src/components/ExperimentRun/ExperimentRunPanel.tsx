import {
  TERMINAL_RUN_STATES,
  experimentRunStatus,
  isTerminalRunStatus,
  normalizeRunStatus,
  type ExperimentRunStatus,
} from '@/constants';
import {
  CaretRightOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  StopOutlined,
} from '@ant-design/icons';
import { Button, Card, Modal, Space, Tooltip, Typography } from 'antd';
import { useEffect, useState } from 'react';
import MetricsPanel from './MetricsPanel';
import RealtimeLogPanel from './RealtimeLogPanel';
import RunStatusBadge from './RunStatusBadge';
import { fiTokens, statusAccent } from './fi-tokens';
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

const cardBody = { padding: '12px 16px' };

/** §5 reduced-motion — suppress the timeline state-transition animation for motion-sensitive users. */
function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return;
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    const apply = () => setReduced(mq.matches);
    apply();
    mq.addEventListener?.('change', apply);
    return () => mq.removeEventListener?.('change', apply);
  }, []);
  return reduced;
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
  experimentUUID,
  status,
  onStatusChanged,
  apiPrefix = '/chaosmeta/api/v1',
}: ExperimentRunPanelProps) {
  const { loading, run } = useExperimentAction({ apiPrefix });
  const normalized = normalizeRunStatus(status);
  const meta = experimentRunStatus[normalized];
  const terminal = TERMINAL_RUN_STATES.includes(normalized);

  const act = async (kind: 'start' | 'stop' | 'pause' | 'resume') => {
    // v3.1 §9.2: start needs experimentUUID; stop/pause/resume need instanceUUID. The hook enforces this.
    const ok = await run(kind, experimentInstanceUUID, experimentUUID);
    if (ok && onStatusChanged) onStatusChanged();
  };

  // v3.1 §9.3: stop must be available from ANY non-finished-clean state, including Error (whose fault
  // may still be resident — that's exactly when we need to force a recover). Only the already-clean
  // terminals (Stopped/Succeeded) disable stop. Error/Failed/Paused/Running/Recovering all allow it.
  const stopDisabled = normalized === 'Stopped' || normalized === 'Succeeded';

  // §5 confirmation-dialogs (HIGH): stop triggers a recover/cleanup, which is a non-trivial side
  // effect (and, from Error, may be the only way to release a resident fault). Confirm before firing.
  const confirmStop = () => {
    const inDangerZone = normalized === 'Error' || normalized === 'Failed';
    Modal.confirm({
      title: '停止实验',
      content: inDangerZone
        ? '当前为异常/失败态，可能仍有故障驻留。停止将触发恢复回收（清理故障资源）。确认执行？'
        : '停止将立即触发恢复回收，清理已注入的故障。确认执行停止？',
      okText: '停止',
      okType: 'danger',
      cancelText: '取消',
      onOk: () => act('stop'),
    });
  };

  return (
    <Space direction="vertical" style={{ width: '100%' }} size={fiTokens.spaceMd}>
      {/* ① Top bar */}
      <Card size="small" styles={{ body: cardBody }} style={{ borderColor: fiTokens.border }}>
        <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
          <Space size={fiTokens.spaceSm} align="center">
            <Typography.Text style={{ fontSize: 15, fontWeight: 600 }}>
              实验运行
            </Typography.Text>
            <RunStatusBadge status={status} />
            <span style={{ color: fiTokens.textSecondary, fontSize: 12 }}>
              {meta.labelUS}
            </span>
            {(normalized === 'Error' || normalized === 'Failed') && (
              <span style={{ color: fiTokens.statusError, fontSize: 12 }}>
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
              disabled={stopDisabled}
              onClick={confirmStop}
            >
              停止
            </Button>
          </Space>
        </Space>
      </Card>

      {/* ①.5 State-machine timeline (Task-2 observability): makes the idle→running→paused→stopped→error
          path visible at a glance, with the current state highlighted via the shared statusAccent. */}
      <Card size="small" styles={{ body: { padding: '10px 16px' } }} style={{ borderColor: fiTokens.border }}>
        <RunStatusTimeline normalized={normalized} />
      </Card>

      {/* ③ Real-time log */}
      <Card size="small" title="实时日志" styles={{ header: { borderBottom: `1px solid ${fiTokens.borderSubtle}` }, body: { padding: fiTokens.spaceMd } }} style={{ borderColor: fiTokens.border }}>
        <RealtimeLogPanel
          experimentInstanceUUID={experimentInstanceUUID}
          apiPrefix={apiPrefix}
        />
      </Card>

      {/* ④ Process data */}
      <Card size="small" title="过程数据" styles={{ header: { borderBottom: `1px solid ${fiTokens.borderSubtle}` }, body: { padding: fiTokens.spaceMd } }} style={{ borderColor: fiTokens.border }}>
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

/**
 * RunStatusTimeline — a compact horizontal state-machine strip: Idle → Running → Paused → Stopped,
 * with Error as an off-ramp marker. The current normalized state is highlighted with the shared
 * statusAccent token; passed-through states are dimmed. This makes "状态机状态迁移" visible without
 * a heavy diagram lib (Task-2 observability, Task-1 美观度).
 *
 * Intentionally lightweight (pure CSS flex + tokens, no runtime compute beyond normalized lookup)
 * so it stays cheap on the 7x24h auto-refresh path.
 */
function RunStatusTimeline({
  normalized,
}: {
  normalized: ExperimentRunStatus;
}) {
  const reducedMotion = usePrefersReducedMotion();
  const steps: { key: ExperimentRunStatus; label: string }[] = [
    { key: 'Idle', label: '待执行' },
    { key: 'Running', label: '运行中' },
    { key: 'Paused', label: '已暂停' },
    { key: 'Stopped', label: '已停止' },
  ];
  const currentIndex = steps.findIndex((s) => s.key === normalized);
  // Failed / Error are terminal-but-not-on-the-happy-path; show them as an off-ramp chip beside the line.
  const isErrorRamp = normalized === 'Failed' || normalized === 'Error';
  const isSucceeded = normalized === 'Succeeded';

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: fiTokens.spaceXs,
        flexWrap: 'wrap',
      }}
    >
      {steps.map((s, i) => {
        const isCurrent = s.key === normalized;
        const isPassed = currentIndex >= 0 && i < currentIndex;
        const accent = statusAccent(s.key);
        const dim = isSucceeded && i < steps.length - 1; // Succeeded quietly lights the line end-to-end
        return (
          <div
            key={s.key}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: fiTokens.spaceXs,
            }}
          >
            <div
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 6,
                padding: '3px 10px',
                borderRadius: fiTokens.radiusPill,
                fontSize: 12,
                fontWeight: isCurrent ? 600 : 400,
                color: isCurrent
                  ? accent
                  : isPassed || dim
                  ? fiTokens.textSecondary
                  : fiTokens.textLogFaint,
                border: `1px solid ${isCurrent ? accent : fiTokens.border}`,
                background: isCurrent ? `${accent}14` : 'transparent',
                transition: reducedMotion ? 'none' : 'all .2s',
              }}
            >
              <span
                style={{
                  width: 7,
                  height: 7,
                  borderRadius: '50%',
                  background:
                    isCurrent || isPassed || dim ? accent : fiTokens.border,
                  display: 'inline-block',
                }}
              />
              {s.label}
            </div>
            {i < steps.length - 1 && (
              <div
                style={{
                  width: 18,
                  height: 1,
                  background:
                    isPassed || dim ? fiTokens.statusSucceed : fiTokens.border,
                }}
              />
            )}
          </div>
        );
      })}
      {isErrorRamp && (
        <div
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 6,
            marginLeft: fiTokens.spaceXs,
            padding: '3px 10px',
            borderRadius: fiTokens.radiusPill,
            fontSize: 12,
            fontWeight: 600,
            color: fiTokens.statusError,
            border: `1px solid ${fiTokens.statusError}`,
            background: `${fiTokens.statusError}14`,
          }}
        >
          <span
            style={{
              width: 7,
              height: 7,
              borderRadius: '50%',
              background: fiTokens.statusError,
              display: 'inline-block',
            }}
          />
          {normalized === 'Error' ? '异常（可能驻留，请停止）' : '失败'}
        </div>
      )}
      {isSucceeded && (
        <div
          style={{
            marginLeft: fiTokens.spaceXs,
            fontSize: 12,
            color: fiTokens.statusSucceed,
            fontWeight: 600,
          }}
        >
          ✓ 成功结束
        </div>
      )}
    </div>
  );
}
