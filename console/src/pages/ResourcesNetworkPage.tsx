import { useEffect, useMemo, useState } from 'react';
import type { ECBasicOption } from 'echarts/types/dist/shared';
import { api } from '../api.ts';
import { EChart } from '../components/EChart.tsx';
import { formatQuantile, formatValue } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type { DailyTrendPoint, GlobalFilters, NetworkReport, ResourceSceneReport } from '../types.ts';

interface Props {
  filters: GlobalFilters;
  refreshTick: number;
  lang: Lang;
}

export function ResourcesNetworkPage({ filters, refreshTick, lang }: Props) {
  const [resources, setResources] = useState<ResourceSceneReport[]>([]);
  const [network, setNetwork] = useState<NetworkReport | null>(null);
  const [trends, setTrends] = useState<DailyTrendPoint[]>([]);
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

  const netTrendOption: ECBasicOption = useMemo(() => ({
    tooltip: { trigger: 'axis', axisPointer: { type: 'cross' } },
    legend: { top: 0, right: 10, textStyle: { fontSize: 11 } },
    grid: { top: 32, right: 48, bottom: 26, left: 48 },
    xAxis: { type: 'category', data: trends.map(p => p.day_utc.slice(5)), axisLabel: { color: '#86909c', fontSize: 11 } },
    yAxis: [
      { type: 'value', name: '%', min: 85, max: 100, splitLine: { lineStyle: { color: '#f2f3f5' } } },
      { type: 'value', name: 'ms', splitLine: { show: false } },
    ],
    series: [
      {
        name: lang === 'zh' ? '逻辑 API 成功率 (%)' : 'API Success (%)',
        type: 'line',
        yAxisIndex: 0,
        smooth: 0.25,
        connectNulls: false,
        data: trends.map(p => p.api_success_rate_pct !== null ? Number(p.api_success_rate_pct.toFixed(2)) : null),
        itemStyle: { color: '#00b42a' },
      },
      {
        name: 'API P90 (ms)',
        type: 'bar',
        yAxisIndex: 1,
        barMaxWidth: 22,
        data: trends.map(p => p.api_p90_ms),
        itemStyle: { color: 'rgba(22,93,255,0.45)', borderRadius: [3, 3, 0, 0] },
      },
    ],
  }), [trends, lang]);

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
      { name: lang === 'zh' ? '内存峰值 P90 (MiB)' : 'Memory P90 (MiB)', type: 'line', yAxisIndex: 0, smooth: 0.25, connectNulls: false, data: trends.map(p => p.memory_p90_mib), itemStyle: { color: '#722ed1' }, areaStyle: { color: 'rgba(114,46,209,0.08)' } },
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
              <h2 className="card-title">{t(lang, 'netHist')}</h2>
              <p className="card-sub">
                P50 {formatQuantile(network?.duration_p50_ms, 'ms').main} · P90 {formatQuantile(network?.duration_p90_ms, 'ms').main}
              </p>
            </div>
          </div>
          <EChart option={netHistOption} height={235} />
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'resTrend')}</h2>
              <p className="card-sub">{lang === 'zh' ? 'Android PSS / iOS phys_footprint 与进程 CPU 走势' : 'Process CPU & resident memory P90 trend'}</p>
            </div>
          </div>
          <EChart option={resTrendOption} height={235} />
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'netWaterfall')}</h2>
            <p className="card-sub">{lang === 'zh' ? '原生 OkHttp EventListener / URLSessionTaskTransactionMetrics 真实建连与首包拆解' : 'OkHttp & URLSession transport timing breakdown'}</p>
          </div>
          <span className="badge blue">{network?.operations.length ?? 0} {lang === 'zh' ? '个接口' : 'endpoints'}</span>
        </div>

        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{lang === 'zh' ? '接口路径' : 'Operation'}</th>
                <th>{lang === 'zh' ? '采样数' : 'Samples'}</th>
                <th>{lang === 'zh' ? '成功率' : 'Success'}</th>
                <th>{lang === 'zh' ? '连接复用率' : 'Reuse'}</th>
                <th>Total P90</th>
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
                  <tr key={`${op.method}-${op.operation}`}>
                    <td className="mono"><span className="badge blue" style={{ marginRight: 6 }}>{op.method}</span><strong>{op.operation}</strong></td>
                    <td className="mono">{op.sample_count.toLocaleString('en-US')}</td>
                    <td className="mono">{formatValue(op.success_rate_pct, '%')}</td>
                    <td className="mono">{formatValue(op.connection_reuse_rate_pct, '%')}</td>
                    <td className="mono"><strong>{formatQuantile(op.total_p90_ms, 'ms').main}</strong></td>
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
                <th>{lang === 'zh' ? '内存口径' : 'Memory Kind'}</th>
                <th>{lang === 'zh' ? '平均 CPU' : 'Avg CPU'}</th>
                <th>Memory P50 / P90</th>
                <th>RN LongTask</th>
                <th>{lang === 'zh' ? '单窗 JS 阻塞 / 峰值' : 'JS Blocking / Max'}</th>
              </tr>
            </thead>
            <tbody>
              {resources.map(r => (
                <tr key={r.scene}>
                  <td><strong>{r.scene}</strong></td>
                  <td className="mono">{r.memory_kind || '—'}</td>
                  <td className="mono">{formatValue(r.avg_cpu_pct, '%', r.cpu_missing_reason)}</td>
                  <td className="mono">{formatQuantile(r.memory_peak_p50_mib, 'MiB').main} / <strong>{formatQuantile(r.memory_peak_p90_mib, 'MiB').main}</strong></td>
                  <td className="mono">{r.js_longtask_count.toLocaleString('en-US')} ({r.js_longtask_windows} win)</td>
                  <td className="mono">{formatValue(r.avg_js_blocking_ms_per_window, 'ms')} / {formatValue(r.js_max_task_ms, 'ms')}</td>
                </tr>
              ))}
              {resources.length === 0 && <tr><td colSpan={6} className="empty">—</td></tr>}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}
