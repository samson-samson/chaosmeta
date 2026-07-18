import {
  experimentRunStatus,
  normalizeRunStatus,
  type ExperimentRunStatus,
} from '@/constants';
import { Tag } from 'antd';

export interface RunStatusBadgeProps {
  /** Any backend / CRD / Argo / legacy-enum status value. The adapter normalizes it. */
  status: string | number | null | undefined;
  /** Optional override of the resolved label (overrides default localized label). */
  label?: string;
  style?: React.CSSProperties;
}

const toneToTagColor: Record<ExperimentRunStatus, string> = {
  Idle: 'default',
  Running: 'processing',
  Paused: 'warning',
  Stopped: 'default',
  Recovering: 'processing',
  Succeeded: 'success',
  Failed: 'error',
  Error: 'error',
};

/**
 * RunStatusBadge — the ONE status pill used across the experiment pages.
 *
 * Replaces the older scattered inline status→color ad-hoc mapping. Accepts any raw status
 * (CRD/chaosmetad/Argo/legacy numeric) and renders a normalized antd Tag.
 *
 * Task 1 / D10 fix.
 */
export default function RunStatusBadge({
  status,
  label,
  style,
}: RunStatusBadgeProps) {
  const normalized = normalizeRunStatus(status);
  const meta = experimentRunStatus[normalized];
  return (
    <Tag
      color={toneToTagColor[normalized]}
      style={style}
      aria-label={`status-${normalized}`}
    >
      {label ?? meta.label}
    </Tag>
  );
}
