import { message, notification } from 'antd';
import { useCallback, useState } from 'react';

type ActionKind = 'start' | 'stop' | 'pause' | 'resume';

export interface UseExperimentActionOpts {
  /** Base API prefix, e.g. '/chaosmeta/api/v1'. */
  apiPrefix?: string;
}

export interface ExperimentActionResult {
  loading: Record<ActionKind, boolean>;
  /**
   * Execute an action. uuidNature differs by action (verified against the real backend routes,
   * see design v3.1 §9.2):
   *   - start  → experiment UUID   (POST /experiments/:experimentUUID/start)
   *   - stop   → instance UUID     (POST /experiments/:instanceUUID/stop  → UserStopExperiment)
   *   - pause  / resume → instance UUID (new routes aligned with stop)
   * The caller must pass both ids; passing a single uuid for all actions was the v3 bug that broke stop.
   */
  run: (
    kind: ActionKind,
    experimentInstanceUUID: string,
    experimentUUID?: string,
  ) => Promise<boolean>;
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
 * Backend verbs (verified, routers/experiment.go):
 *   - start  : POST {apiPrefix}/experiments/:experimentUUID/start   (experiment UUID!)
 *   - stop   : POST {apiPrefix}/experiments/:instanceUUID/stop      (instance UUID!)
 *   - pause  : POST {apiPrefix}/experiments/:instanceUUID/pause     (new, aligned with stop)
 *   - resume : POST {apiPrefix}/experiments/:instanceUUID/resume    (new)
 * start therefore needs the EXPERIMENT uuid; stop / pause / resume need the INSTANCE uuid.
 * A 404 is treated as "action channel not wired yet" and surfaced as a clear warning rather than a crash,
 * so pages ship safely ahead of the backend.
 *
 * G0 (v3.1 §9.1): stop now works from Running / Paused / Error after the webhook + routine放开.
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
      experimentUUID?: string,
    ): Promise<boolean> => {
      // v3.1 §9.2: start needs the EXPERIMENT uuid; the rest need the INSTANCE uuid.
      const routeUUID =
        kind === 'start' ? experimentUUID : experimentInstanceUUID;
      // codex-review-2: pause/resume are intentionally NOT backed — the operator has no pause phase
      // handler (solveFinalStatus only acts on recover), so wiring a route would be an empty shell.
      // Surfacing an honest "暂未启用" notice is better than a 404 or a fake success. stop/start are real.
      if (kind === 'pause' || kind === 'resume') {
        notification.warning({
          message: `${kind === 'pause' ? '暂停' : '恢复'}暂未启用`,
          description:
            '底层 operator 暂未落地 pause-phase-handler（需 chaosmetad 进程级 SIGSTOP 原语 + 集群验证）。停止/启动可用。',
        });
        return false;
      }
      if (!routeUUID) {
        notification.error({
          message: `${kind} 失败`,
          description:
            kind === 'start'
              ? '缺少实验 UUID，无法启动'
              : '缺少实验实例 UUID，无法操作',
        });
        return false;
      }
      setLoading((s) => ({ ...s, [kind]: true }));
      const hide = message.loading(LOADING_LABEL[kind], 0);
      try {
        const path = `${apiPrefix}/experiments/${encodeURIComponent(
          routeUUID,
        )}/${kind}`;
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
