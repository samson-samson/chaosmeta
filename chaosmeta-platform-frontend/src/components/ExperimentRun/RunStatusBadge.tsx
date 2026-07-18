import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  ExclamationCircleOutlined,
  LoadingOutlined,
  MinusCircleOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
} from '@ant-design/icons';
import {
  experimentRunStatus,
  normalizeRunStatus,
  type ExperimentRunStatus,
} from '@/constants';
import { Tag } from 'antd';
import { fiTokens } from './fi-tokens';
import type { CSSProperties } from 'react';

export interface RunStatusBadgeProps {
  /** Any backend / CRD / Argo / legacy-enum status value. The adapter normalizes it. */
  status: string | number | null | undefined;
  /** Optional override of the resolved label (overrides default localized label). */
  label?: string;
  /** Hide the leading status icon (e.g. in dense table cells). Default: shown. */
  hideIcon?: boolean;
  style?: CSSProperties;
}

/** antd Tag preset per normalized tone. Kept distinct from statusAccent (used by the timeline). */
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
 * Status iconography. §1 a11y (color-not-only): every tone pairs its color with a distinct glyph so
 * meaning survives red/green colour blindness and a Tag printed in monochrome. One icon per tone —
 * no glyph is reused across two tones that share a colour (Stopped≠Succeeded both green=default, so
 * Stopped uses Minus while Succeeded uses Check).
 */
function StatusGlyph({ tone }: { tone: ExperimentRunStatus }) {
  const common = { style: { marginRight: 4 } as CSSProperties };
  switch (tone) {
    case 'Running':
    case 'Recovering':
      return <LoadingOutlined {...common} />;
    case 'Paused':
      return <PauseCircleOutlined {...common} />;
    case 'Stopped':
      return <MinusCircleOutlined {...common} />;
    case 'Succeeded':
      return <CheckCircleOutlined {...common} />;
    case 'Failed':
      return <CloseCircleOutlined {...common} />;
    case 'Error':
      return <ExclamationCircleOutlined {...common} />;
    case 'Idle':
    default:
      return <PlayCircleOutlined {...common} />;
  }
}

/**
 * RunStatusBadge — the ONE status pill used across the experiment pages.
 *
 * Replaces the older scattered inline status→color ad-hoc mapping. Accepts any raw status
 * (CRD/chaosmetad/Argo/legacy numeric) and renders a normalized antd Tag with a leading icon so the
 * state is legible without colour (WCAG 1.4.1 / design system §1).
 *
 * Task 1 / D10 fix; iconography added in the ui-ux-pro-max pass (color-not-only).
 */
export default function RunStatusBadge({
  status,
  label,
  hideIcon,
  style,
}: RunStatusBadgeProps) {
  const normalized = normalizeRunStatus(status);
  const meta = experimentRunStatus[normalized];
  return (
    <Tag
      color={toneToTagColor[normalized]}
      style={{ borderRadius: fiTokens.radiusPill, ...style }}
      aria-label={`status-${normalized}`}
    >
      {hideIcon ? null : <StatusGlyph tone={normalized} />}
      {label ?? meta.label}
    </Tag>
  );
}
