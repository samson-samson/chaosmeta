import { Card, Col, Empty, Row, Spin, Table } from 'antd';
import * as echarts from 'echarts';
import { useEffect, useMemo, useRef, useState } from 'react';
import { fiTokens } from './fi-tokens';

export interface MetricsData {
  successRate: number; // 0..1
  total: number;
  succeeded: number;
  failed: number;
  latency?: {
    p50?: number;
    p90?: number;
    p99?: number;
    max?: number;
    buckets?: { le: number; v: number }[];
  };
  errors?: { type: string; count: number }[];
  nodes?: { node: string; inject: number; recover: number; fail: number }[];
}

export interface MetricsPanelProps {
  experimentInstanceUUID: string;
  apiPrefix?: string;
}

const fmtPct = (r: number) => `${(r * 100).toFixed(1)}%`;

void fmtPct; // reserved for future status-card use; keep the helper centralized here.

/**
 * MetricsPanel — key process-data visualization for an experiment instance.
 *
 * Replaces the legacy ObservationCharts.tsx commented-out fake-data placeholder (D12). Shows:
 *   - injection success-rate gauge (echarts gauge)
 *   - latency distribution histogram (p50/p90/p99 + bucket bars)
 *   - error counts by type (bar)
 *   - per-node inject/recover/fail breakdown table
 *
 * Resilience (non-functional requirements):
 *   - a non-200 / 404 response shows a graceful "过程数据暂不可用" empty state instead of crashing (D13)
 *   - missing optional sections (latency/errors/nodes) render partial panels, never throw
 *   - echarts instances are disposed on unmount to avoid the long-run handle leak (7x24h requirement)
 *
 * Backend contract (design §1.5): GET {apiPrefix}/experiments/:instanceUUID/metrics → MetricsData.
 * Task 1 / D12 + D13.
 */
export default function MetricsPanel({
  experimentInstanceUUID,
  apiPrefix = '/chaosmeta/api/v1',
}: MetricsPanelProps) {
  const [data, setData] = useState<MetricsData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>('');

  const gaugeRef = useRef<HTMLDivElement>(null);
  const latencyRef = useRef<HTMLDivElement>(null);
  const errorsRef = useRef<HTMLDivElement>(null);
  const charts = useRef<echarts.ECharts[]>([]);

  const load = async () => {
    try {
      const url = `${apiPrefix}/experiments/${encodeURIComponent(
        experimentInstanceUUID,
      )}/metrics`;
      const resp = await fetch(url, {
        headers: { Accept: 'application/json' },
      });
      if (resp.status === 404) {
        setError('过程数据通道未就绪（后端 API 尚未接入）');
        setData(null);
        return;
      }
      if (!resp.ok) {
        setError(`拉取过程数据失败 (${resp.status})`);
        return;
      }
      setError('');
      setData((await resp.json()) as MetricsData);
    } catch (e) {
      setError(`网络错误: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    setLoading(true);
    load();
    // refresh ~every 5s for live runs; cheap, server may cache.
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [experimentInstanceUUID, apiPrefix]);

  // Render charts whenever data changes. Dispose previous instances to avoid handle leaks.
  useEffect(() => {
    charts.current.forEach((c) => c.dispose());
    charts.current = [];
    if (!data) return;

    if (gaugeRef.current) {
      const c = echarts.init(gaugeRef.current);
      c.setOption({
        series: [
          {
            type: 'gauge',
            startAngle: 200,
            endAngle: -20,
            min: 0,
            max: 100,
            progress: { show: true, width: 14, itemStyle: { color: fiTokens.chartPrimary } },
            axisLine: { lineStyle: { width: 14, color: [[1, fiTokens.border]] } },
            axisTick: { show: false },
            splitLine: { lineStyle: { color: fiTokens.border } },
            axisLabel: { color: fiTokens.textTertiary, distance: 14, fontSize: 10 },
            detail: {
              valueAnimation: true,
              formatter: '{value}%',
              fontSize: 26,
              fontWeight: 700,
              offsetCenter: [0, '40%'],
              color: fiTokens.textPrimary,
            },
            data: [
              {
                value: Number((data.successRate * 100).toFixed(1)),
                name: '注入成功率',
              },
            ],
          },
        ],
      });
      charts.current.push(c);
    }

    const lat = data.latency;
    if (latencyRef.current) {
      const c = echarts.init(latencyRef.current);
      const buckets = lat?.buckets ?? [];
      // Prefer histogram buckets when present; otherwise synthesize a percentile bar set so there is
      // always exactly one, uniformly-shaped series for echarts.
      const hasBuckets = buckets.length > 0;
      const xLabels = hasBuckets
        ? buckets.map((b) => `≤${b.le}`)
        : ['p50', 'p90', 'p99', 'max'];
      const values = hasBuckets
        ? buckets.map((b) => b.v)
        : [lat?.p50 ?? 0, lat?.p90 ?? 0, lat?.p99 ?? 0, lat?.max ?? 0];
      c.setOption({
        tooltip: { trigger: 'axis', valueFormatter: (v: number) => `${v} ms` },
        xAxis: { type: 'category', data: xLabels, axisLine: { lineStyle: { color: fiTokens.border } } },
        yAxis: { type: 'value', name: 'ms', splitLine: { lineStyle: { color: fiTokens.borderSubtle } } },
        series: [
          { type: 'bar', data: values, itemStyle: { color: fiTokens.chartPrimary, borderRadius: [4, 4, 0, 0] } },
        ],
        grid: { left: 48, right: 16, top: 24, bottom: 32 },
      });
      charts.current.push(c);
    }

    if (errorsRef.current && data.errors?.length) {
      const c = echarts.init(errorsRef.current);
      c.setOption({
        tooltip: { trigger: 'axis' },
        xAxis: {
          type: 'category',
          data: data.errors.map((e) => e.type),
          axisLabel: { rotate: 20 },
          axisLine: { lineStyle: { color: fiTokens.border } },
        },
        yAxis: {
          type: 'value',
          splitLine: { lineStyle: { color: fiTokens.borderSubtle } },
        },
        series: [
          {
            type: 'bar',
            data: data.errors.map((e) => e.count),
            itemStyle: { color: fiTokens.chartNegative, borderRadius: [4, 4, 0, 0] },
          },
        ],
        grid: { left: 40, right: 16, top: 16, bottom: 48 },
      });
      charts.current.push(c);
    }

    const onResize = () => charts.current.forEach((c) => c.resize());
    window.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('resize', onResize);
    };
  }, [data]);

  const nodeColumns = useMemo(
    () => [
      { title: '节点', dataIndex: 'node', key: 'node' },
      {
        title: '注入',
        dataIndex: 'inject',
        key: 'inject',
        render: (v: number) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{v}</span>,
      },
      {
        title: '恢复',
        dataIndex: 'recover',
        key: 'recover',
        render: (v: number) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{v}</span>,
      },
      {
        title: '失败',
        dataIndex: 'fail',
        key: 'fail',
        render: (v: number) =>
          v > 0 ? (
            <span style={{ color: fiTokens.chartNegative, fontVariantNumeric: 'tabular-nums', fontWeight: 600 }}>
              {v}
            </span>
          ) : (
            <span style={{ fontVariantNumeric: 'tabular-nums' }}>0</span>
          ),
      },
    ],
    [],
  );

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 48 }}>
        <Spin />
      </div>
    );
  }
  if (error && !data) {
    return (
      <Card size="small" style={{ borderColor: fiTokens.border }}>
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={error} />
      </Card>
    );
  }
  if (!data) {
    return <Empty description="暂无过程数据" />;
  }

  const cardHead = { padding: '8px 16px', borderBottom: `1px solid ${fiTokens.borderSubtle}` };
  const cardBody = { padding: 12 };

  return (
    <Row gutter={[12, 12]}>
      <Col xs={24} sm={12} md={6}>
        <Card size="small" title="注入成功率" styles={{ header: cardHead, body: { ...cardBody, height: 188 } }}>
          <div ref={gaugeRef} style={{ height: 152 }} />
          <div
            style={{
              textAlign: 'center',
              color: fiTokens.textSecondary,
              fontSize: 12,
              fontVariantNumeric: 'tabular-nums',
              marginTop: 2,
            }}
          >
            共 {data.total} · 成功 {data.succeeded} · 失败 {data.failed}
          </div>
        </Card>
      </Col>
      <Col xs={24} sm={12} md={9}>
        <Card size="small" title="延迟分布 (ms)" styles={{ header: cardHead, body: { ...cardBody, height: 188 } }}>
          <div ref={latencyRef} style={{ height: 160 }} />
        </Card>
      </Col>
      <Col xs={24} md={9}>
        <Card size="small" title="错误计数" styles={{ header: cardHead, body: { ...cardBody, height: 188 } }}>
          {data.errors?.length ? (
            <div ref={errorsRef} style={{ height: 160 }} />
          ) : (
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description="无错误"
              style={{ marginTop: 40 }}
            />
          )}
        </Card>
      </Col>
      <Col span={24}>
        <Card size="small" title="节点维度明细" styles={{ header: cardHead, body: { padding: 0 } }}>
          <Table
            size="small"
            rowKey="node"
            columns={nodeColumns}
            dataSource={data.nodes ?? []}
            pagination={false}
            locale={{ emptyText: '无节点明细' }}
          />
        </Card>
      </Col>
    </Row>
  );
}
