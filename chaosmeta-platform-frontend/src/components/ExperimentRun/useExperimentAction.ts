import { message, notification } from 'antd';
import { useCallback, useState } from 'react';

type ActionKind = 'start' | 'stop' | 'pause' | 'resume';

export interface UseExperimentActionOpts {
  /** Base API prefix, e.g. '/chaosmeta/api/v1'. */
  apiPrefix?: string;
}

export interface ExperimentActionResult {
  loading: Record<ActionKind, boolean>;
  run: (kind: ActionKind, experimentInstanceUUID: string) => Promise<boolean>;
}

const LOADING_LABEL: Record<ActionKind, string> = {
  start: '启动中',
  stop: '停止中',
  pause: '暂停中',
  resume: '恢复中',
};

const SUCCESS_LABEL: Record<ActionKind, string> = {
  start: '已启动',
  stop: '已停止',
  pause: '已暂停',
  resume: '已恢复',
};

/**
 * useExperimentAction — the single hook for the page action buttons (start/stop/pause/resume).
 *
 * Gives every action explicit three-state feedback required by Task 1:
 *   - loading: flips a per-action pending flag (button shows spin + disables)
 *   - success: `message.success` toast
 *   - error:   `notification.error` with the server / network reason — NEVER silently swallowed (D13)
 *
 * Deploys a best-effort PUT/POST to the backend stop/start endpoints. The exact verb/path can vary
 * by deployment; the default matches design §1.6. A 404 is treated as "action channel not wired yet"
 * and surfaced as a clear notification rather than a crash, so pages ship safely ahead of the backend.
 */
export default function useExperimentAction({
  apiPrefix = '/chaosmeta/api/v1',
}: UseExperimentActionOpts = {}): ExperimentActionResult {
  const [loading, setLoading] = useState<Record<ActionKind, boolean>>({
    start: false,
    stop: false,
    pause: false,
    resume: false,
  });

  const run = useCallback(
    async (
      kind: ActionKind,
      experimentInstanceUUID: string,
    ): Promise<boolean> => {
      setLoading((s) => ({ ...s, [kind]: true }));
      const hide = message.loading(LOADING_LABEL[kind], 0);
      try {
        // Align with the real backend verbs/paths:
        //   start/stop exist:  POST {apiPrefix}/experiments/:uuid/start | /stop  (see routers/experiment.go)
        //   pause/resume are not yet backed (design §2.1.1: pause only for process-type faults, chaosmetad
        //   has no pause primitive yet). They hit a best-effort path and surface a clear 404 warning (D13)
        //   rather than pretending success.
        const verb =
          kind === 'stop' ? 'stop' : kind === 'start' ? 'start' : kind; // pause / resume — backend not wired
        const path = `${apiPrefix}/experiments/${encodeURIComponent(
          experimentInstanceUUID,
        )}/${verb}`;
        const resp = await fetch(path, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
        });
        hide();
        if (resp.status === 404) {
          // Backend endpoint not yet wired — surface clearly, do not pretend success.
          notification.warning({
            message: `${kind} 接口未就绪`,
            description: '后端尚未接入该操作通道，请稍后或联系管理员。',
          });
          return false;
        }
        if (!resp.ok) {
          let detail = `HTTP ${resp.status}`;
          try {
            const body = await resp.json();
            detail = body?.message ?? body?.error ?? detail;
          } catch {
            /* body not JSON */
          }
          notification.error({ message: `${kind} 失败`, description: detail });
          return false;
        }
        message.success(SUCCESS_LABEL[kind]);
        return true;
      } catch (e) {
        hide();
        notification.error({
          message: `${kind} 失败`,
          description: (e as Error).message,
        });
        return false;
      } finally {
        setLoading((s) => ({ ...s, [kind]: false }));
      }
    },
    [apiPrefix],
  );

  return { loading, run };
}
