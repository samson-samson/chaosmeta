import {
  CaretDownOutlined,
  DownloadOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
} from '@ant-design/icons';
import {
  Button,
  Empty,
  Input,
  Segmented,
  Space,
  Spin,
  Tooltip,
  message,
} from 'antd';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

export interface LogLine {
  /** Monotonic server-side id — used to resume a tail without dupes. */
  id?: number;
  node: string;
  level: 'info' | 'warn' | 'error' | string;
  phase?: string;
  message: string;
  createdAt?: string;
}

export interface RealtimeLogPanelProps {
  experimentInstanceUUID: string;
  /** Base API prefix, e.g. '/chaosmeta/api/v1'. */
  apiPrefix?: string;
  /** Controlled height (px). Default 420. */
  height?: number;
}

const LEVEL_FILTERS = [
  { label: '全部', value: '' },
  { label: 'Info', value: 'info' },
  { label: 'Warn', value: 'warn' },
  { label: 'Error', value: 'error' },
];

const LEVEL_COLOR: Record<string, string> = {
  info: 'rgba(255,255,255,0.70)',
  warn: '#ffb020',
  error: '#ff5c5c',
  debug: 'rgba(255,255,255,0.45)',
};

/**
 * RealtimeLogPanel — real-time operation log for an experiment instance.
 *
 * Replaces the legacy ShowLog.tsx one-shot AceEditor dump (D11). Features:
 *   - streaming tail via SSE (`text/event-stream`) where the backend supports it,
 *     with an automatic long-poll fallback when SSE is unavailable (graceful degradation, D13)
 *   - level filter (info/warn/error) and node filter
 *   - pause/resume auto-scroll; in-memory line cap to bound memory on 7x24h runs
 *   - non-200 responses surface as an in-panel error banner rather than being silently swallowed (D13)
 *   - export current buffered log to a .txt file
 *
 * Backend contract expected (design §1.4):
 *   GET {apiPrefix}/experiments/:instanceUUID/logs?level=&node=&sinceId=&follow=1
 *     follow=1 → text/event-stream of JSON `LogLine` frames; follow=0 → JSON array history.
 * If that endpoint 404s (backend not yet wired), the panel shows a clear "日志通道未就绪" empty state
 * instead of crashing — so the page ships safely ahead of the backend.
 *
 * Task 1 / D11 + D13.
 */
export default function RealtimeLogPanel({
  experimentInstanceUUID,
  apiPrefix = '/chaosmeta/api/v1',
  height = 420,
}: RealtimeLogPanelProps) {
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [level, setLevel] = useState('');
  const [nodeFilter, setNodeFilter] = useState('');
  const [paused, setPaused] = useState(false);
  const [loading, setLoading] = useState(true);
  const [channelError, setChannelError] = useState<string>('');
  const [sseSupported, setSseSupported] = useState<boolean>(true);

  const scrollRef = useRef<HTMLDivElement>(null);
  const lastIdRef = useRef<number>(0);

  const fetchHistory = useCallback(async () => {
    try {
      const url = `${apiPrefix}/experiments/${encodeURIComponent(
        experimentInstanceUUID,
      )}/logs?follow=0&limit=1000`;
      const resp = await fetch(url, {
        headers: { Accept: 'application/json' },
      });
      if (resp.status === 404) {
        setChannelError('日志通道未就绪（后端 API 尚未接入）');
        setSseSupported(false);
        return [] as LogLine[];
      }
      if (!resp.ok) {
        // D13: surface, don't swallow.
        setChannelError(`拉取历史日志失败 (${resp.status})`);
        return [] as LogLine[];
      }
      setChannelError('');
      const data = (await resp.json()) as { data?: LogLine[] } | LogLine[];
      const arr = Array.isArray(data) ? data : data.data ?? [];
      return arr;
    } catch (e) {
      setChannelError(`网络错误: ${(e as Error).message}`);
      return [] as LogLine[];
    }
  }, [apiPrefix, experimentInstanceUUID]);

  // Initial history pull.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fetchHistory().then((arr) => {
      if (cancelled) return;
      setLogs(arr);
      if (arr.length) {
        lastIdRef.current = arr[arr.length - 1].id ?? lastIdRef.current;
      }
      setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [fetchHistory]);

  // Streaming tail: prefer SSE; fall back to long-poll when SSE/endpoint unavailable.
  useEffect(() => {
    if (channelError && !sseSupported) return; // endpoint missing — do not poll against a void.
    if (sseSupported) {
      const url = `${apiPrefix}/experiments/${encodeURIComponent(
        experimentInstanceUUID,
      )}/logs?follow=1&level=${level}&node=${encodeURIComponent(nodeFilter)}`;
      let es: EventSource | null = null;
      try {
        es = new EventSource(url);
      } catch {
        setSseSupported(false);
        return;
      }
      es.onmessage = (ev) => {
        if (paused) return;
        try {
          const line = JSON.parse(ev.data) as LogLine;
          setLogs((prev) => {
            const next = [...prev, line];
            // bound memory: keep last 5000 lines for 7x24h safety.
            if (next.length > 5000) next.splice(0, next.length - 5000);
            if (line.id) lastIdRef.current = line.id;
            return next;
          });
        } catch {
          /* a malformed frame is non-fatal; skip */
        }
      };
      es.onerror = () => {
        // SSE broke (network blip, or server stopped streaming). Fall back to polling.
        setSseSupported(false);
        es?.close();
      };
      return () => es?.close();
    }
    // Long-poll fallback.
    const pollMs = 3000;
    const t = setInterval(async () => {
      if (paused) return;
      try {
        const url = `${apiPrefix}/experiments/${encodeURIComponent(
          experimentInstanceUUID,
        )}/logs?follow=0&level=${level}&node=${encodeURIComponent(
          nodeFilter,
        )}&sinceId=${lastIdRef.current}&limit=500`;
        const resp = await fetch(url, {
          headers: { Accept: 'application/json' },
        });
        if (!resp.ok) {
          setChannelError(`轮询失败 (${resp.status})`);
          return;
        }
        setChannelError('');
        const data = (await resp.json()) as { data?: LogLine[] } | LogLine[];
        const arr = Array.isArray(data) ? data : data.data ?? [];
        if (arr.length) {
          setLogs((prev) => {
            const next = [...prev, ...arr];
            if (next.length > 5000) next.splice(0, next.length - 5000);
            lastIdRef.current = arr[arr.length - 1].id ?? lastIdRef.current;
            return next;
          });
        }
      } catch {
        /* transient; next tick retries */
      }
    }, pollMs);
    return () => clearInterval(t);
  }, [
    apiPrefix,
    experimentInstanceUUID,
    level,
    nodeFilter,
    paused,
    channelError,
    sseSupported,
  ]);

  // Auto-scroll to bottom unless paused.
  useEffect(() => {
    if (paused) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [logs, paused]);

  const filtered = useMemo(
    () =>
      logs
        .filter((l) => (level ? l.level === level : true))
        .filter((l) =>
          nodeFilter ? (l.node ?? '').includes(nodeFilter) : true,
        ),
    [logs, level, nodeFilter],
  );

  const exportLogs = () => {
    const text = filtered
      .map(
        (l) =>
          `[${l.createdAt ?? ''}] [${l.level.toUpperCase()}] [${l.node}] ${
            l.message
          }`,
      )
      .join('\n');
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `experiment-${experimentInstanceUUID}-logs.txt`;
    a.click();
    URL.revokeObjectURL(url);
    message.success('已导出');
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
        <Space>
          <Segmented
            value={level}
            onChange={(v) => setLevel(v as string)}
            options={LEVEL_FILTERS}
            size="small"
          />
          <Input
            allowClear
            placeholder="按节点过滤"
            size="small"
            style={{ width: 160 }}
            value={nodeFilter}
            onChange={(e) => setNodeFilter(e.target.value)}
          />
        </Space>
        <Space>
          <Tooltip title={paused ? '继续滚动' : '暂停滚动'}>
            <Button
              size="small"
              icon={paused ? <PlayCircleOutlined /> : <PauseCircleOutlined />}
              onClick={() => setPaused((p) => !p)}
            >
              {paused ? '继续' : '暂停'}
            </Button>
          </Tooltip>
          <Button size="small" icon={<DownloadOutlined />} onClick={exportLogs}>
            导出
          </Button>
          {!channelError && !paused && (
            <Tooltip title="实时跟随">
              <CaretDownOutlined style={{ color: '#52c41a' }} />
            </Tooltip>
          )}
        </Space>
      </Space>

      {channelError && (
        <div
          style={{
            color: '#ff5c5c',
            fontSize: 12,
            padding: '4px 8px',
            background: 'rgba(255,92,92,0.08)',
            borderRadius: 6,
          }}
        >
          {channelError}
        </div>
      )}

      <div
        ref={scrollRef}
        style={{
          height,
          overflow: 'auto',
          background: '#1e1e1e',
          borderRadius: 8,
          padding: '12px 14px',
          fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
          fontSize: 12.5,
          lineHeight: 1.7,
          border: '1px solid rgba(255,255,255,0.06)',
        }}
      >
        {loading ? (
          <div style={{ textAlign: 'center', paddingTop: 40 }}>
            <Spin />
          </div>
        ) : filtered.length === 0 ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={channelError || '暂无日志'}
            style={{ marginTop: 80 }}
          />
        ) : (
          filtered.map((l, idx) => (
            <div
              key={l.id ?? `${idx}:${l.message.slice(0, 8)}`}
              style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}
            >
              <span style={{ color: 'rgba(255,255,255,0.35)', marginRight: 8 }}>
                {l.createdAt ?? ''}
              </span>
              <span style={{ color: 'rgba(255,255,255,0.45)', marginRight: 8 }}>
                [{l.node || '-'}]
              </span>
              <span
                style={{
                  color: LEVEL_COLOR[l.level] ?? 'rgba(255,255,255,0.7)',
                  marginRight: 8,
                  fontWeight: 600,
                }}
              >
                {l.level.toUpperCase()}
              </span>
              <span style={{ color: 'rgba(255,255,255,0.85)' }}>
                {l.message}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
