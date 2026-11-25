import { useEffect, useMemo, useState } from 'react';
import type { ECBasicOption } from 'echarts/types/dist/shared';
import { api } from '../api.ts';
import { EChart } from '../components/EChart.tsx';
import { formatQuantile, formatTime, formatValue } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type {
  DailyTrendPoint,
  GlobalFilters,
  NetworkReport,
  NetworkTransportSample,
  ResourceSceneReport,
} from '../types.ts';

interface Props {
  filters: GlobalFilters;
  refreshTick: number;
  lang: Lang;
  onTraceUser?: (targetType: 'user_hash' | 'install_hash', hash: string) => void;
}

export function ResourcesNetworkPage({ filters, refreshTick, lang, onTraceUser }: Props) {
  const [resources, setResources] = useState<ResourceSceneReport[]>([]);
  const [network, setNetwork] = useState<NetworkReport | null>(null);
  const [trends, setTrends] = useState<DailyTrendPoint[]>([]);
  const [selectedOp, setSelectedOp] = useState<string | null>(null);
  const [onlyIssues, setOnlyIssues] = useState(true);
  const [samples, setSamples] = useState<NetworkTransportSample[]>([]);
  const [loadingSamples, setLoadingSamples] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([api.resources(filters), api.network(filters), api.timeseries(filters)])
      .then(([resData, netData, tsData]) => {
        if (!active) return;
        setResources(resData.scenes ?? []);
        setNetwork(netData);
        setTrends(tsData.points ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [JSON.stringify(filters), refreshTick]);

  useEffect(() => {
    if (!selectedOp) return;
    let active = true;
    setLoadingSamples(true);
    api.networkSamples(filters, selectedOp === '__ALL__' ? undefined : selectedOp, onlyIssues, 50)
      .then(res => {
        if (!active) return;
        setSamples(res.samples ?? []);
      })
      .catch(err => { if (active) setError(String(err)); })
      .finally(() => { if (active) setLoadingSamples(false); });
    return () => { active = false; };
  }, [selectedOp, onlyIssues, JSON.stringify(filters), refreshTick]);

  const netTrendOption: ECBasicOption = useMemo(() => {
    const validRates = trends.map(p => p.api_success_rate_pct).filter((v): v is number => v !== null);
    const minRate = validRates.length > 0 ? Math.min(...validRates) : 95;
    const yMin = Math.max(80, Math.min(96, Math.floor(minRate - 1.2)));
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'cross' } },
      legend: { top: 0, left: 'center', textStyle: { fontSize: 11 } },
      grid: { top: 44, right: 48, bottom: 26, left: 48 },
      xAxis: { type: 'category', data: trends.map(p => p.day_utc.slice(5)), axisLabel: { color: '#86909c', fontSize: 11 } },
      yAxis: [
        { type: 'value', name: '%', min: yMin, max: 100, splitLine: { lineStyle: { color: '#f2f3f5' } } },
        { type: 'value', name: 'ms', splitLine: { show: false } },
      ],
      series: [
        {
          name: lang === 'zh' ? '逻辑 API 成功率 (%)' : 'API Success (%)',
          type: 'line',
          yAxisIndex: 0,
          smooth: 0.28,
          connectNulls: false,
          data: trends.map(p => p.api_success_rate_pct !== null ? Number(p.api_success_rate_pct.toFixed(2)) : null),
          itemStyle: { color: '#00b42a' },
        },
        {
          name: 'API P90 (ms)',
          type: 'bar',
          yAxisIndex: 1,
          barMaxWidth: 22,
          data: trends.map(p => p.api_p90_ms !== null ? Math.round(p.api_p90_ms) : null),
          itemStyle: { color: 'rgba(22,93,255,0.42)', borderRadius: [3, 3, 0, 0] },
        },
      ],
    };
  }, [trends, lang]);

  const errorDonutOption: ECBasicOption = useMemo(() => {
    const fb = network?.failure_breakdown ?? {};
    const items = [
      { name: 'HTTP 4xx/5xx', value: fb.http ?? 0, itemStyle: { color: '#f53f3f' } },
      { name: lang === 'zh' ? '超时 Timeout' : 'Timeout', value: fb.timeout ?? 0, itemStyle: { color: '#ff7d00' } },
      { name: lang === 'zh' ? '网络层 Network' : 'Network', value: fb.network ?? 0, itemStyle: { color: '#722ed1' } },
      { name: lang === 'zh' ? '解析 Parse' : 'Parse', value: fb.parse ?? 0, itemStyle: { color: '#0fc6c2' } },
      { name: lang === 'zh' ? '业务码 Business' : 'Business', value: fb.business ?? 0, itemStyle: { color: '#165dff' } },
    ].filter(x => x.value > 0);
    return {
      tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
      legend: { bottom: 0, textStyle: { fontSize: 11 } },
      series: [
        {
          type: 'pie',
          radius: ['46%', '72%'],
          center: ['50%', '44%'],
          label: { show: false },
          data: items.length > 0 ? items : [{ name: 'No Failures', value: 1, itemStyle: { color: '#e5e6eb' } }],
        },
      ],
    };
  }, [network, lang]);

  const statusCodeOption: ECBasicOption = useMemo(() => {
    const sc = network?.status_codes ?? {};
    const keys = [
      { key: '2xx', label: '2xx OK', color: '#00b42a' },
      { key: '3xx', label: '3xx Cache', color: '#0fc6c2' },
      { key: '4xx', label: '4xx Client', color: '#ff7d00' },
      { key: '5xx', label: '5xx Server', color: '#f53f3f' },
      { key: 'timeout', label: 'Timeout', color: '#722ed1' },
    ];
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
      grid: { top: 20, right: 16, bottom: 26, left: 48 },
      xAxis: { type: 'category', data: keys.map(k => k.label), axisLabel: { color: '#4e5969', fontSize: 11 } },
      yAxis: { type: 'value', name: lang === 'zh' ? '样本数' : 'Samples', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      series: [
        {
          type: 'bar',
          barMaxWidth: 32,
          data: keys.map(k => ({
            value: sc[k.key] ?? 0,
            itemStyle: { color: k.color, borderRadius: [3, 3, 0, 0] },
          })),
          label: { show: true, position: 'top', fontSize: 11, color: '#4e5969' },
        },
      ],
    };
  }, [network, lang]);

  const netHistOption: ECBasicOption = useMemo(() => {
    const buckets = (network?.duration_buckets ?? []).filter(b => b.count > 0 || (b.upper !== null && b.upper <= 3000));
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
      grid: { top: 22, right: 16, bottom: 42, left: 48 },
      xAxis: { type: 'category', data: buckets.map(b => b.label), axisLabel: { color: '#86909c', fontSize: 10, rotate: 28 } },
      yAxis: { type: 'value', name: lang === 'zh' ? '请求数' : 'Calls', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      series: [
        {
          type: 'bar',
          barMaxWidth: 24,
          data: buckets.map(b => ({
            value: b.count,
            itemStyle: {
              color: b.lower >= 1000 ? '#f53f3f' : b.lower >= 500 ? '#ff7d00' : '#0fc6c2',
              borderRadius: [3, 3, 0, 0],
            },
          })),
        },
      ],
    };
  }, [network, lang]);

  const resTrendOption: ECBasicOption = useMemo(() => ({
    tooltip: { trigger: 'axis', axisPointer: { type: 'cross' } },
    legend: { top: 0, right: 10, textStyle: { fontSize: 11 } },
    grid: { top: 32, right: 48, bottom: 26, left: 48 },
    xAxis: { type: 'category', data: trends.map(p => p.day_utc.slice(5)), axisLabel: { color: '#86909c', fontSize: 11 } },
    yAxis: [
      { type: 'value', name: 'MiB', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      { type: 'value', name: 'CPU %', splitLine: { show: false } },
    ],
    series: [
      { name: lang === 'zh' ? '内存峰值 P95 (MiB)' : 'Memory P95 (MiB)', type: 'line', yAxisIndex: 0, smooth: 0.28, connectNulls: false, data: trends.map(p => p.memory_p95_mib !== null ? Math.round(p.memory_p95_mib) : null), itemStyle: { color: '#a871e3' }, lineStyle: { width: 1.6, type: 'dashed' } },
      { name: lang === 'zh' ? '内存峰值 P90 (MiB)' : 'Memory P90 (MiB)', type: 'line', yAxisIndex: 0, smooth: 0.28, connectNulls: false, data: trends.map(p => p.memory_p90_mib !== null ? Math.round(p.memory_p90_mib) : null), itemStyle: { color: '#722ed1' }, areaStyle: { color: 'rgba(114,46,209,0.08)' } },
      { name: lang === 'zh' ? '进程平均 CPU (%)' : 'Avg CPU (%)', type: 'line', yAxisIndex: 1, smooth: 0.25, connectNulls: false, data: trends.map(p => p.avg_cpu_pct !== null ? Number(p.avg_cpu_pct.toFixed(1)) : null), itemStyle: { color: '#165dff' } },
    ],
  }), [trends, lang]);

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}

      <div className="grid-3-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'netTrend')}</h2>
              <p className="card-sub">
                {lang === 'zh'
                  ? `全量逻辑请求分母 ${network?.total_calls.toLocaleString('en-US') ?? 0} 次 · 失败 ${network?.failed_calls.toLocaleString('en-US') ?? 0} 次`
                  : `${network?.total_calls.toLocaleString('en-US') ?? 0} logical calls · ${network?.failed_calls.toLocaleString('en-US') ?? 0} failed`}
              </p>
            </div>
            <span className={`badge ${(network?.success_rate_pct ?? 100) < 98 ? 'red' : 'green'}`}>
              {formatValue(network?.success_rate_pct, '%', network?.missing_reason)}
            </span>
          </div>
          <EChart option={netTrendOption} height={240} />
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'netErrDonut')}</h2>
              <p className="card-sub">{lang === 'zh' ? '按 5 类失败域精细归因（HTTP / 超时 / 网络 / 解析 / 业务）' : '5-domain failure classification'}</p>
            </div>
            <span className="badge red">{network?.failed_calls.toLocaleString('en-US') ?? 0}</span>
          </div>
          <EChart option={errorDonutOption} height={240} />
        </div>
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{lang === 'zh' ? 'HTTP 状态码与超时分布 (Status Code Distribution)' : 'HTTP Status Code & Timeout Distribution'}</h2>
              <p className="card-sub">{lang === 'zh' ? '基于原生网络传输采样统计 2xx / 3xx / 4xx / 5xx / Timeout 分布' : 'Observed 2xx / 3xx / 4xx / 5xx / Timeout transport breakdown'}</p>
            </div>
            <button className="btn" onClick={() => { setOnlyIssues(true); setSelectedOp('__ALL__'); }}>
              {lang === 'zh' ? '查看慢/异常请求样本 ↗' : 'Inspect Slow/Error Samples ↗'}
            </button>
          </div>
          <EChart option={statusCodeOption} height={235} />
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'netHist')}</h2>
              <p className="card-sub">
                P50 {formatQuantile(network?.duration_p50_ms, 'ms').main} · P90 {formatQuantile(network?.duration_p90_ms, 'ms').main}
              </p>
            </div>
          </div>
          <EChart option={netHistOption} height={235} />
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'netWaterfall')}</h2>
            <p className="card-sub">{lang === 'zh' ? '原生 OkHttp / URLSession 真实建连与首包拆解 · 点击任意接口下钻慢请求与异常样本' : 'OkHttp & URLSession transport timing · click any endpoint to inspect slow/error request samples'}</p>
          </div>
          <span className="badge blue">{network?.operations.length ?? 0} {lang === 'zh' ? '个接口' : 'endpoints'}</span>
        </div>

        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{lang === 'zh' ? '接口路径 (点击下钻)' : 'Operation (Click to Drill)'}</th>
                <th>{lang === 'zh' ? '采样数' : 'Samples'}</th>
                <th>{lang === 'zh' ? '成功率' : 'Success'}</th>
                <th>{lang === 'zh' ? '连接复用率' : 'Reuse'}</th>
                <th>Total P90 / P99</th>
                <th>{lang === 'zh' ? '阶段瀑布 (DNS / TCP / TLS / TTFB / Body)' : 'Transport Waterfall'}</th>
              </tr>
            </thead>
            <tbody>
              {(network?.operations ?? []).map(op => {
                const dns = op.dns_p90_ms.value ?? 0;
                const tcp = op.tcp_p90_ms.value ?? 0;
                const tls = op.tls_p90_ms.value ?? 0;
                const ttfb = op.ttfb_p90_ms.value ?? 0;
                const body = op.body_p90_ms.value ?? 0;
                const sum = Math.max(dns + tcp + tls + ttfb + body, 1);
                return (
                  <tr
                    key={`${op.method}-${op.operation}`}
                    className="clickable"
                    onClick={() => { setOnlyIssues(true); setSelectedOp(op.operation); }}
                  >
                    <td className="mono">
                      <span className="badge blue" style={{ marginRight: 6 }}>{op.method}</span>
                      <strong>{op.operation}</strong>
                      <span className="kpi-meta" style={{ marginLeft: 6 }}>↗</span>
                    </td>
                    <td className="mono">{op.sample_count.toLocaleString('en-US')}</td>
                    <td className="mono">{formatValue(op.success_rate_pct, '%')}</td>
                    <td className="mono">{formatValue(op.connection_reuse_rate_pct, '%')}</td>
                    <td className="mono">
                      <strong>{formatQuantile(op.total_p90_ms, 'ms').main}</strong>
                      <div className="kpi-meta">P99 {formatQuantile(op.total_p99_ms, 'ms').main}</div>
                    </td>
                    <td>
                      <div className="bar-track">
                        <div className="bar-seg" style={{ width: `${(dns / sum) * 100}%`, background: '#0fc6c2' }} title={`DNS ${dns}ms`} />
                        <div className="bar-seg" style={{ width: `${(tcp / sum) * 100}%`, background: '#165dff' }} title={`TCP ${tcp}ms`} />
                        <div className="bar-seg" style={{ width: `${(tls / sum) * 100}%`, background: '#722ed1' }} title={`TLS ${tls}ms`} />
                        <div className="bar-seg" style={{ width: `${(ttfb / sum) * 100}%`, background: '#ff7d00' }} title={`TTFB ${ttfb}ms`} />
                        <div className="bar-seg" style={{ width: `${(body / sum) * 100}%`, background: '#00b42a' }} title={`Body ${body}ms`} />
                      </div>
                      <div className="mono kpi-meta">
                        DNS {Math.round(dns)} · TCP {Math.round(tcp)} · TLS {Math.round(tls)} · TTFB {Math.round(ttfb)} · Body {Math.round(body)} ms
                      </div>
                    </td>
                  </tr>
                );
              })}
              {(network?.operations.length ?? 0) === 0 && <tr><td colSpan={6} className="empty">—</td></tr>}
            </tbody>
          </table>
        </div>
        <div className="legend">
          <span className="legend-item"><span className="swatch" style={{ background: '#0fc6c2' }} />DNS</span>
          <span className="legend-item"><span className="swatch" style={{ background: '#165dff' }} />TCP</span>
          <span className="legend-item"><span className="swatch" style={{ background: '#722ed1' }} />TLS</span>
          <span className="legend-item"><span className="swatch" style={{ background: '#ff7d00' }} />TTFB</span>
          <span className="legend-item"><span className="swatch" style={{ background: '#00b42a' }} />Body</span>
        </div>
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'resTrend')}</h2>
              <p className="card-sub">{lang === 'zh' ? 'Android PSS / iOS phys_footprint 与进程 CPU 走势' : 'Process CPU & resident memory P90 trend'}</p>
            </div>
          </div>
          <EChart option={resTrendOption} height={235} />
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'resSceneTable')}</h2>
              <p className="card-sub">{lang === 'zh' ? '原生进程资源 + 可选 React Native JS 主线程阻塞 (LongTask) 同表归因' : 'Native process CPU/Memory alongside optional RN JS LongTasks'}</p>
            </div>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === 'zh' ? '业务场景' : 'Scene'}</th>
                  <th>{lang === 'zh' ? '平均 CPU' : 'Avg CPU'}</th>
                  <th>Memory P90 / P95</th>
                  <th>RN LongTask</th>
                  <th>{lang === 'zh' ? '单窗 JS 阻塞' : 'JS Block'}</th>
                </tr>
              </thead>
              <tbody>
                {resources.map(r => (
                  <tr key={r.scene}>
                    <td><strong>{r.scene}</strong><div className="mono kpi-meta">{r.memory_kind || '—'}</div></td>
                    <td className="mono">{formatValue(r.avg_cpu_pct, '%', r.cpu_missing_reason)}</td>
                    <td className="mono">
                      <strong>{formatQuantile(r.memory_peak_p90_mib, 'MiB').main}</strong>
                      <div className="kpi-meta">P95 {formatQuantile(r.memory_peak_p95_mib, 'MiB').main}</div>
                    </td>
                    <td className="mono">{r.js_longtask_count.toLocaleString('en-US')}</td>
                    <td className="mono">{formatValue(r.avg_js_blocking_ms_per_window, 'ms')}</td>
                  </tr>
                ))}
                {resources.length === 0 && <tr><td colSpan={5} className="empty">—</td></tr>}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      {selectedOp && (
        <div className="drawer-backdrop" onClick={() => setSelectedOp(null)}>
          <div className="drawer" onClick={e => e.stopPropagation()}>
            <div className="card-header" style={{ marginBottom: 0 }}>
              <div>
                <h2 className="card-title">
                  {lang === 'zh' ? '慢请求与异常请求单次瀑布样本' : 'Slow & Error Request Samples'}
                </h2>
                <div className="mono kpi-meta">
                  {selectedOp === '__ALL__' ? (lang === 'zh' ? '全部接口' : 'All Endpoints') : selectedOp} · {samples.length} {lang === 'zh' ? '条抽样' : 'samples'}
                </div>
              </div>
              <div className="filter-group">
                <div className="sub-tabs">
                  <button className={onlyIssues ? 'active' : ''} onClick={() => setOnlyIssues(true)}>
                    {lang === 'zh' ? '仅慢/失败 (≥800ms/4xx+)' : 'Slow & Errors'}
                  </button>
                  <button className={!onlyIssues ? 'active' : ''} onClick={() => setOnlyIssues(false)}>
                    {lang === 'zh' ? '全部样本' : 'All Samples'}
                  </button>
                </div>
                <button className="btn" onClick={() => setSelectedOp(null)}>{lang === 'zh' ? '关闭' : 'Close'}</button>
              </div>
            </div>

            {loadingSamples ? (
              <div className="empty">Loading…</div>
            ) : samples.length === 0 ? (
              <div className="empty">{lang === 'zh' ? '当前筛选条件下暂无符合条件的请求样本。' : 'No matching network samples.'}</div>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                {samples.slice(0, 30).map(s => {
                  const dns = s.dns_ms ?? 0;
                  const tcp = s.tcp_ms ?? 0;
                  const tls = s.tls_ms ?? 0;
                  const ttfb = s.ttfb_ms ?? 0;
                  const body = s.body_ms ?? 0;
                  const sum = Math.max(dns + tcp + tls + ttfb + body, s.total_ms, 1);
                  const bad = s.result !== 'success' || (s.status_code ?? 0) >= 400;
                  return (
                    <div key={s.event_id} style={{ padding: 10, border: '1px solid var(--border)', borderRadius: 8, background: 'var(--surface)' }}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 6, marginBottom: 6 }}>
                        <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                          <span className={`badge ${bad ? 'red' : s.total_ms >= 800 ? 'amber' : 'green'}`}>
                            {s.result === 'timeout' ? 'TIMEOUT' : `HTTP ${s.status_code || s.result}`}
                          </span>
                          <span className="mono"><strong>{s.method} {s.operation}</strong></span>
                          <span className="badge slate">v{s.app_version}</span>
                          {s.protocol && <span className="badge blue">{s.protocol}</span>}
                          {s.network_type && <span className="badge slate">{s.network_type}</span>}
                          {s.trace_id && (
                            <button
                              type="button"
                              className="badge purple"
                              style={{ border: 'none', cursor: 'pointer', fontFamily: 'var(--mono)' }}
                              title={`W3C traceparent: 00-${s.trace_id}-${s.span_id || '0000000000000001'}-01 (click to copy trace_id)`}
                              onClick={() => navigator.clipboard?.writeText(s.trace_id || '')}
                            >
                              trace:{s.trace_id.slice(0, 8)}…
                            </button>
                          )}
                        </div>
                        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                          <strong className="mono" style={{ color: bad ? 'var(--red)' : s.total_ms >= 800 ? 'var(--amber)' : 'var(--text)' }}>
                            {Math.round(s.total_ms)} ms
                          </strong>
                          {onTraceUser && (s.user_hash || s.install_hash) && (
                            <button
                              className="btn ghost"
                              style={{ padding: '2px 8px', fontSize: 11 }}
                              onClick={() => {
                                const isUser = Boolean(s.user_hash);
                                const hash = s.user_hash || s.install_hash || '';
                                setSelectedOp(null);
                                onTraceUser(isUser ? 'user_hash' : 'install_hash', hash);
                              }}
                            >
                              {lang === 'zh' ? '单点追查 ↗' : 'Trace ↗'}
                            </button>
                          )}
                        </div>
                      </div>
                      <div className="bar-track" style={{ marginBottom: 5 }}>
                        <div className="bar-seg" style={{ width: `${(dns / sum) * 100}%`, background: '#0fc6c2' }} title={`DNS ${Math.round(dns)}ms`} />
                        <div className="bar-seg" style={{ width: `${(tcp / sum) * 100}%`, background: '#165dff' }} title={`TCP ${Math.round(tcp)}ms`} />
                        <div className="bar-seg" style={{ width: `${(tls / sum) * 100}%`, background: '#722ed1' }} title={`TLS ${Math.round(tls)}ms`} />
                        <div className="bar-seg" style={{ width: `${(ttfb / sum) * 100}%`, background: '#ff7d00' }} title={`TTFB ${Math.round(ttfb)}ms`} />
                        <div className="bar-seg" style={{ width: `${(body / sum) * 100}%`, background: '#00b42a' }} title={`Body ${Math.round(body)}ms`} />
                      </div>
                      <div className="mono kpi-meta">
                        DNS {Math.round(dns)} · TCP {Math.round(tcp)} · TLS {Math.round(tls)} · TTFB {Math.round(ttfb)} · Body {Math.round(body)} ms
                        {' · '}{s.device_model || '—'} ({s.os_version || '—'})
                        {' · '}{formatTime(s.occurred_at_ms).slice(5, 19)}
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>
      )}
    </>
  );
}
