/**
 * fi-tokens — design tokens for the fault-injection pages ONLY (Task 1 "美观度", v3.1 §5.2).
 *
 * Scope discipline: these tokens are NOT a global theme. They live beside the ExperimentRun
 * components and replace the previously hard-coded colors scattered across RealtimeLogPanel /
 * MetricsPanel / ExperimentRunPanel (the "#ff5c5c", "#1e1e1e", "#5b8ff9" inline literals).
 * Centralizing them here keeps the explosion radius to the fault-injection pages — other Space
 * pages are untouched.
 *
 * Palette is intentionally calm and high-contrast: a single neutral surface scale plus a status
 * color ramp aligned to the 8 normalized run states (see constants/index.ts experimentRunStatus).
 */

export const fiTokens = {
  // surfaces (light enterprise — aligned with antd5 defaults, NOT a dark page override)
  surface: '#ffffff',
  surfaceAlt: '#f7f8fa',
  pageBg: '#f8fafc', // §2 page background (light enterprise)
  surfaceMuted: '#f1f5f9', // §2 muted bucket / empty-state fill
  surfaceLog: '#0f1115', // terminal-style log background (the ONE dark surface, by design)

  // borders / dividers (modernized Slate scale, §2)
  border: '#e2e8f0',
  borderSubtle: '#f1f5f9',
  borderStrong: '#cbd5e1',

  // text (§2, all WCAG AAA on surface)
  textPrimary: '#0f172a',
  textSecondary: '#475569',
  textTertiary: '#94a3b8',
  textLog: 'rgba(255, 255, 255, 0.85)',
  textLogDim: 'rgba(255, 255, 255, 0.45)',
  textLogFaint: 'rgba(255, 255, 255, 0.35)',

  // status ramp (aligned to experimentRunStatus tones; color is NEVER the sole signal — see RunStatusBadge icons)
  statusRunning: '#1677ff',
  statusPaused: '#fa8c16',
  statusSucceed: '#16a34a', // §2 semantic success (slightly deeper for AAA on white)
  statusFailed: '#dc2626', // §2 destructive
  statusError: '#cf1322',
  statusIdle: '#94a3b8',

  // data / accent scale (§2 — blue data + amber accent; used by charts + highlights, not antd theme)
  dataPrimary: '#1e40af',
  dataSecondary: '#3b82f6',
  accent: '#d97706',

  // log levels
  levelInfo: 'rgba(255, 255, 255, 0.70)',
  levelWarn: '#ffb020',
  levelError: '#ff5c5c',
  levelDebug: 'rgba(255, 255, 255, 0.45)',

  // chart palette (harmonized §4 set; applied via tokens so charts and badges agree)
  chartPrimary: '#3b82f6', // §2 data-secondary as the primary chart line/bar
  chartSecondary: '#1e40af', // §2 data-primary for paired series
  chartNegative: '#dc2626', // §2 destructive for fail/anomaly
  chartWarn: '#d97706', // §2 accent for warning buckets
  chartPositive: '#16a34a', // success series

  // radii (§3 — restrained, not fully Swiss-square)
  radiusCard: 8,
  radiusPill: 4,
  radiusInput: 6,

  // shadows (§3 — none by default; cards separate by 1px border + bg diff, not shadow)
  shadowFloat: '0 6px 16px rgba(15, 23, 42, 0.08)',

  // mono stack for metrics / logs / uuids (§3 — tabular figures, no Google Font fetch)
  fontMono:
    'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace',

  // spacing scale (§3 — 8dp rhythm)
  spaceXs: 8,
  spaceSm: 12,
  spaceMd: 16,
  spaceLg: 24,
  spaceXl: 32,
} as const;

export type FiToken = typeof fiTokens;

/**
 * Map a normalized run status to its accent token. Single source of truth so the badge, the
 * progress timeline, and any future accent use all agree on color.
 */
export function statusAccent(
  status:
    | 'Idle'
    | 'Running'
    | 'Paused'
    | 'Stopped'
    | 'Recovering'
    | 'Succeeded'
    | 'Failed'
    | 'Error',
): string {
  switch (status) {
    case 'Running':
    case 'Recovering':
      return fiTokens.statusRunning;
    case 'Paused':
      return fiTokens.statusPaused;
    case 'Succeeded':
      return fiTokens.statusSucceed;
    case 'Stopped':
      return fiTokens.textSecondary; // §2: terminal-normal, not green — stopped ≠ succeeded
    case 'Failed':
      return fiTokens.statusFailed;
    case 'Error':
      return fiTokens.statusError;
    case 'Idle':
    default:
      return fiTokens.statusIdle;
  }
}
