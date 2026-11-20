import { useEffect, useMemo, useState } from 'react';
import type { ECBasicOption } from 'echarts/types/dist/shared';
import { api } from '../api.ts';
import { EChart, Sparkline } from '../components/EChart.tsx';
import { formatDelta, formatPValue, formatQuantile, formatValue, metricLabel, missingLabel, statTestLabel } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type {
  CompareReport,
  DailyTrendPoint,
  DimensionSlice,
  GlobalFilters,
  OverviewReport,
  RegressionScanReport,
  ReleaseRecord,
} from '../types.ts';

interface Props {
  filters: GlobalFilters;
  releases: ReleaseRecord[];
  refreshTick: number;
  lang: Lang;
}

type TrendMetricTab = 'startup' | 'fluency' | 'network' | 'resources' | 'stability';

export function OverviewPage({ filters, releases, refreshTick, lang }: Props) {
  const [overview, setOverview] = useState<OverviewReport | null>(null);
  const [trends, setTrends] = useState<DailyTrendPoint[]>([]);
  const [verBreakdown, setVerBreakdown] = useState<DimensionSlice[]>([]);
  const [adapterBreakdown, setAdapterBreakdown] = useState<DimensionSlice[]>([]);
  const [compare, setCompare] = useState<CompareReport | null>(null);
  const [scan, setScan] = useState<RegressionScanReport | null>(null);
  const [trendTab, setTrendTab] = useState<TrendMetricTab>('startup');
  const [error, setError] = useState('');

  const uniqueVersions = useMemo(() => Array.from(new Set(releases.map(r => r.app_version))).sort(), [releases]);
  const [baseline, setBaseline] = useState('');
  const [candidate, setCandidate] = useState('');

  useEffect(() => {
    if (uniqueVersions.length >= 2) {
      if (!baseline || !uniqueVersions.includes(baseline)) setBaseline(uniqueVersions[0]);
      if (!candidate || !uniqueVersions.includes(candidate)) setCandidate(uniqueVersions[1]);
    }
  }, [uniqueVersions.join(',')]);

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([
      api.overview(filters),
      api.timeseries(filters),
      api.breakdown(filters, 'version'),
      api.breakdown(filters, 'adapter'),
    ])
      .then(([ov, ts, vb, ab]) => {
        if (!active) return;
        setOverview(ov);
        setTrends(ts.points ?? []);
        setVerBreakdown(vb.slices ?? []);
        setAdapterBreakdown(ab.slices ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [JSON.stringify(filters), refreshTick]);

  useEffect(() => {
    if (!baseline || !candidate) return;
    let active = true;
    Promise.all([
      api.compare(filters, baseline, candidate),
      api.scanRegressions(filters, baseline, candidate),
    ])
      .then(([cmpData, scanData]) => {
        if (!active) return;
        setCompare(cmpData);
        setScan(scanData);
      })
      .catch(() => {
        if (!active) return;
        setCompare(null);
        setScan(null);
      });
    return () => { active = false; };
  }, [filters.platform, filters.env, filters.scene, filters.startDayUtc, filters.endDayUtc, baseline, candidate, refreshTick]);

  const startupP90 = formatQuantile(overview?.startup.total_p90_ms, 'ms');
  const netP90 = formatQuantile(overview?.network.duration_p90_ms, 'ms');

  const validFl = overview?.fluency.filter(f => f.valid_windows > 0) ?? [];
  const totalFrames = validFl.reduce((a, b) => a + b.total_frames, 0);
  const jankFrames = validFl.reduce((a, b) => a + b.jank_frames, 0);
  const zeroFrameWins = (overview?.fluency ?? []).reduce((a, b) => a + b.zero_frame_windows, 0);
  const overallJank = totalFrames > 0 ? (jankFrames / totalFrames) * 100 : null;

  const peakMemMiB = useMemo(() => {
    let maxVal: number | null = null;
    for (const r of overview?.resources ?? []) {
      if (r.memory_peak_p90_mib.value !== null) {
        if (maxVal === null || r.memory_peak_p90_mib.value > maxVal) {
          maxVal = r.memory_peak_p90_mib.value;
        }
      }
    }
    return maxVal;
  }, [overview]);

  const crashEvents = (overview?.top_issues ?? []).reduce((a, b) => a + b.event_count, 0);

  // Release rollout markers (detect day when dominant_version changes!)
  const rolloutMarkLines = useMemo(() => {
    const lines: Array<{ xAxis: string; name: string }> = [];
    for (let i = 1; i < trends.length; i++) {
      const prev = trends[i - 1].dominant_version;
      const curr = trends[i].dominant_version;
      if (curr && prev && curr !== prev) {
        lines.push({ xAxis: trends[i].day_utc.slice(5), name: `v${curr}` });
      }
    }
    return lines;
  }, [trends]);

  const heroChartOption: ECBasicOption = useMemo(() => {
    const xData = trends.map(p => p.day_utc.slice(5));
    const markLine = rolloutMarkLines.length > 0
      ? {
          symbol: 'none',
          lineStyle: { type: 'dashed', color: '#86909c', width: 1.5 },
          label: { formatter: '{b}', fontSize: 11, color: '#4e5969', backgroundColor: '#f2f3f5', padding: [2, 5], borderRadius: 3 },
          data: rolloutMarkLines,
        }
      : undefined;

    const baseGrid = { top: 36, right: 54, bottom: 26, left: 52 };
    const tooltip = { trigger: 'axis', axisPointer: { type: 'cross' } };

    if (trendTab === 'startup') {
      return {
        tooltip,
        legend: { top: 2, right: 12, textStyle: { fontSize: 11 } },
        grid: baseGrid,
        xAxis: { type: 'category', data: xData, axisLine: { lineStyle: { color: '#e5e6eb' } }, axisLabel: { color: '#86909c', fontSize: 11 } },
        yAxis: { type: 'value', name: 'ms', nameTextStyle: { color: '#86909c' }, splitLine: { lineStyle: { color: '#f2f3f5' } } },
        series: [
          { name: 'Cold Start P90', type: 'line', smooth: 0.25, connectNulls: false, data: trends.map(p => p.startup_p90_ms), itemStyle: { color: '#165dff' }, areaStyle: { color: 'rgba(22,93,255,0.08)' }, markLine },
          { name: 'Cold Start P50', type: 'line', smooth: 0.25, connectNulls: false, data: trends.map(p => p.startup_p50_ms), itemStyle: { color: '#0fc6c2' } },
        ],
      };
    }
    if (trendTab === 'fluency') {
      return {
        tooltip,
        legend: { top: 2, right: 12, textStyle: { fontSize: 11 } },
        grid: baseGrid,
        xAxis: { type: 'category', data: xData, axisLabel: { color: '#86909c', fontSize: 11 } },
        yAxis: { type: 'value', name: '%', splitLine: { lineStyle: { color: '#f2f3f5' } } },
        series: [
          {
            name: lang === 'zh' ? '卡顿率 (%)' : 'Jank Rate (%)',
            type: 'line',
            smooth: 0.25,
            connectNulls: false,
            data: trends.map(p => p.jank_rate_pct !== null ? Number(p.jank_rate_pct.toFixed(2)) : null),
            itemStyle: { color: '#ff7d00' },
            areaStyle: { color: 'rgba(255,125,0,0.10)' },
            markLine,
          },
        ],
      };
    }
    if (trendTab === 'network') {
      return {
        tooltip,
        legend: { top: 2, right: 12, textStyle: { fontSize: 11 } },
        grid: baseGrid,
        xAxis: { type: 'category', data: xData, axisLabel: { color: '#86909c', fontSize: 11 } },
        yAxis: [
          { type: 'value', name: '%', min: 85, max: 100, splitLine: { lineStyle: { color: '#f2f3f5' } } },
          { type: 'value', name: 'ms', splitLine: { show: false } },
        ],
        series: [
          { name: lang === 'zh' ? 'API 成功率 (%)' : 'API Success (%)', type: 'line', yAxisIndex: 0, smooth: 0.25, connectNulls: false, data: trends.map(p => p.api_success_rate_pct !== null ? Number(p.api_success_rate_pct.toFixed(2)) : null), itemStyle: { color: '#00b42a' }, markLine },
          { name: 'API P90 (ms)', type: 'bar', yAxisIndex: 1, barMaxWidth: 24, data: trends.map(p => p.api_p90_ms), itemStyle: { color: 'rgba(22,93,255,0.45)', borderRadius: [3, 3, 0, 0] } },
        ],
      };
    }
    if (trendTab === 'resources') {
      return {
        tooltip,
        legend: { top: 2, right: 12, textStyle: { fontSize: 11 } },
        grid: baseGrid,
        xAxis: { type: 'category', data: xData, axisLabel: { color: '#86909c', fontSize: 11 } },
        yAxis: [
          { type: 'value', name: 'MiB', splitLine: { lineStyle: { color: '#f2f3f5' } } },
          { type: 'value', name: 'CPU %', splitLine: { show: false } },
        ],
        series: [
          { name: lang === 'zh' ? '内存峰值 P90 (MiB)' : 'Memory P90 (MiB)', type: 'line', yAxisIndex: 0, smooth: 0.25, connectNulls: false, data: trends.map(p => p.memory_p90_mib), itemStyle: { color: '#722ed1' }, areaStyle: { color: 'rgba(114,46,209,0.08)' }, markLine },
          { name: lang === 'zh' ? '平均 CPU (%)' : 'Avg CPU (%)', type: 'line', yAxisIndex: 1, smooth: 0.25, connectNulls: false, data: trends.map(p => p.avg_cpu_pct !== null ? Number(p.avg_cpu_pct.toFixed(1)) : null), itemStyle: { color: '#0fc6c2' } },
        ],
      };
    }
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
      legend: { top: 2, right: 12, textStyle: { fontSize: 11 } },
      grid: baseGrid,
      xAxis: { type: 'category', data: xData, axisLabel: { color: '#86909c', fontSize: 11 } },
      yAxis: { type: 'value', name: lang === 'zh' ? '次数' : 'Events', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      series: [
        { name: 'Crash', type: 'bar', stack: 'stab', barMaxWidth: 26, data: trends.map(p => p.crash_events), itemStyle: { color: '#f53f3f' }, markLine },
        { name: 'ANR', type: 'bar', stack: 'stab', barMaxWidth: 26, data: trends.map(p => p.anr_events), itemStyle: { color: '#ff7d00' } },
        { name: 'Abnormal Exit', type: 'bar', stack: 'stab', barMaxWidth: 26, data: trends.map(p => p.abnormal_exit_events), itemStyle: { color: '#722ed1', borderRadius: [3, 3, 0, 0] } },
      ],
    };
  }, [trends, trendTab, rolloutMarkLines, lang]);

  const versionChartOption: ECBasicOption = useMemo(() => ({
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    legend: { top: 0, right: 8, textStyle: { fontSize: 11 } },
    grid: { top: 32, right: 48, bottom: 24, left: 48 },
    xAxis: { type: 'category', data: verBreakdown.map(s => `v${s.key}`), axisLabel: { color: '#4e5969', fontSize: 11 } },
    yAxis: [
      { type: 'value', name: 'ms', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      { type: 'value', name: '%', splitLine: { show: false } },
    ],
    series: [
      { name: lang === 'zh' ? '冷启动 P90 (ms)' : 'Startup P90', type: 'bar', yAxisIndex: 0, barMaxWidth: 28, data: verBreakdown.map(s => s.startup_p90_ms), itemStyle: { color: '#165dff', borderRadius: [3, 3, 0, 0] } },
      { name: lang === 'zh' ? '卡顿率 (%)' : 'Jank %', type: 'line', yAxisIndex: 1, data: verBreakdown.map(s => s.jank_rate_pct !== null ? Number(s.jank_rate_pct.toFixed(2)) : null), itemStyle: { color: '#f53f3f' }, lineStyle: { width: 2.5 } },
    ],
  }), [verBreakdown, lang]);

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}

      <div className="kpi-grid-5">
        <div className="kpi-card">
          <div className="kpi-head">
            <span className="kpi-label">{t(lang, 'kpiColdStart')}</span>
            <span className={`badge ${(overview?.startup.total_p90_ms.value ?? 0) > 1000 ? 'red' : 'green'}`}>
              n={overview?.startup.success_count.toLocaleString('en-US') ?? 0}
            </span>
          </div>
          <div className="kpi-value">{startupP90.main}</div>
          <Sparkline values={trends.map(p => p.startup_p90_ms)} color="#165dff" />
          <div className="kpi-meta">{startupP90.sub}</div>
        </div>

        <div className="kpi-card">
          <div className="kpi-head">
            <span className="kpi-label">{t(lang, 'kpiJankRate')}</span>
            <span className={`badge ${(overallJank ?? 0) >= 5 ? 'red' : (overallJank ?? 0) >= 3 ? 'amber' : 'green'}`}>
              {totalFrames.toLocaleString('en-US')} {lang === 'zh' ? '帧' : 'frames'}
            </span>
          </div>
          <div className="kpi-value">{formatValue(overallJank, '%', 'no_observations')}</div>
          <Sparkline values={trends.map(p => p.jank_rate_pct)} color="#ff7d00" />
          <div className="kpi-meta">
            {lang === 'zh' ? `已隔离 ${zeroFrameWins} 个零帧窗口` : `${zeroFrameWins} zero-frame windows excluded`}
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-head">
            <span className="kpi-label">{t(lang, 'kpiApiSuccess')}</span>
            <span className={`badge ${(overview?.network.success_rate_pct ?? 100) < 98 ? 'red' : 'green'}`}>
              P90 {netP90.main}
            </span>
          </div>
          <div className="kpi-value">
            {formatValue(overview?.network.success_rate_pct, '%', overview?.network.missing_reason)}
          </div>
          <Sparkline values={trends.map(p => p.api_success_rate_pct)} color="#00b42a" />
          <div className="kpi-meta">
            {(overview?.network.total_calls ?? 0).toLocaleString('en-US')} {lang === 'zh' ? '次逻辑请求' : 'logical calls'}
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-head">
            <span className="kpi-label">{t(lang, 'kpiMemP90')}</span>
            <span className={`badge ${(peakMemMiB ?? 0) > 320 ? 'amber' : 'blue'}`}>
              {filters.platform === 'ios' ? 'phys_footprint' : 'PSS'}
            </span>
          </div>
          <div className="kpi-value">{formatValue(peakMemMiB, 'MiB', 'no_observations')}</div>
          <Sparkline values={trends.map(p => p.memory_p90_mib)} color="#722ed1" />
          <div className="kpi-meta">
            {overview?.resources.length ?? 0} {lang === 'zh' ? '个活跃业务场景' : 'active scenes'}
          </div>
        </div>

        <div className="kpi-card">
          <div className="kpi-head">
            <span className="kpi-label">{t(lang, 'kpiStability')}</span>
            <span className="badge red">{overview?.top_issues.length ?? 0} {lang === 'zh' ? '类聚合' : 'issues'}</span>
          </div>
          <div className="kpi-value">{crashEvents.toLocaleString('en-US')}</div>
          <Sparkline values={trends.map(p => p.crash_events + p.anr_events + p.abnormal_exit_events)} color="#f53f3f" />
          <div className="kpi-meta">
            {lang === 'zh' ? '含 Crash / ANR / 异常退出' : 'Crash · ANR · Abnormal Exit'}
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'trendTitle')}</h2>
            <p className="card-sub">
              {lang === 'zh'
                ? '虚线标注版本主流量切换日（可一眼看出 v1.1.0 发布劣化与 v1.2.0 修复回落）'
                : 'Dashed markers denote dominant release rollout transitions across calendar days'}
            </p>
          </div>
          <div className="sub-tabs">
            <button className={trendTab === 'startup' ? 'active' : ''} onClick={() => setTrendTab('startup')}>{t(lang, 'tabStartup')}</button>
            <button className={trendTab === 'fluency' ? 'active' : ''} onClick={() => setTrendTab('fluency')}>{t(lang, 'tabFluency')}</button>
            <button className={trendTab === 'network' ? 'active' : ''} onClick={() => setTrendTab('network')}>{t(lang, 'tabNetwork')}</button>
            <button className={trendTab === 'resources' ? 'active' : ''} onClick={() => setTrendTab('resources')}>{t(lang, 'tabResources')}</button>
            <button className={trendTab === 'stability' ? 'active' : ''} onClick={() => setTrendTab('stability')}>{t(lang, 'tabStability')}</button>
          </div>
        </div>
        {trends.length > 0 ? (
          <EChart option={heroChartOption} height={285} />
        ) : (
          <div className="empty">{lang === 'zh' ? '当前筛选条件下暂无按日趋势数据。' : 'No daily trend points in current scope.'}</div>
        )}
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'versionBreakdown')}</h2>
              <p className="card-sub">{lang === 'zh' ? '按版本横向对比冷启动 P90 与流畅度卡顿率' : 'Side-by-side Cold Start P90 & Jank Rate per release'}</p>
            </div>
            <span className="badge blue">{verBreakdown.length} {lang === 'zh' ? '个版本' : 'releases'}</span>
          </div>
          {verBreakdown.length > 0 ? <EChart option={versionChartOption} height={225} /> : <div className="empty">—</div>}
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'adapterBreakdown')}</h2>
              <p className="card-sub">{lang === 'zh' ? 'Native 核心与 React Native 混合页独立归因对比' : 'Pure Native foundation vs React Native adapter slice'}</p>
            </div>
            <span className="badge purple">Native + RN</span>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === 'zh' ? '技术栈' : 'Stack'}</th>
                  <th>{lang === 'zh' ? '冷启动 P90' : 'Startup P90'}</th>
                  <th>{lang === 'zh' ? '卡顿率' : 'Jank %'}</th>
                  <th>{lang === 'zh' ? 'API 成功率' : 'API OK %'}</th>
                  <th>{lang === 'zh' ? '内存 P90' : 'Mem P90'}</th>
                </tr>
              </thead>
              <tbody>
                {adapterBreakdown.map(s => (
                  <tr key={s.key}>
                    <td>
                      <span className={`badge ${s.key === 'native' ? 'blue' : 'purple'}`}>
                        {s.key === 'native' ? (lang === 'zh' ? '纯 Native' : 'Native') : 'React Native'}
                      </span>
                    </td>
                    <td className="mono">{formatValue(s.startup_p90_ms, 'ms')}</td>
                    <td className="mono">{formatValue(s.jank_rate_pct, '%')}</td>
                    <td className="mono">{formatValue(s.api_success_rate_pct, '%')}</td>
                    <td className="mono">{formatValue(s.memory_p90_mib, 'MiB')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'compareTitle')}</h2>
            <p className="card-sub">{t(lang, 'compareSub')}</p>
          </div>
          <div className="filter-group">
            <label className="filter-chip">
              <span>{t(lang, 'baseline')}</span>
              <select value={baseline} onChange={e => setBaseline(e.target.value)}>
                {uniqueVersions.map(v => <option key={v} value={v}>v{v}</option>)}
              </select>
            </label>
            <label className="filter-chip">
              <span>{t(lang, 'candidate')}</span>
              <select value={candidate} onChange={e => setCandidate(e.target.value)}>
                {uniqueVersions.map(v => <option key={v} value={v}>v{v}</option>)}
              </select>
            </label>
          </div>
        </div>

        {compare ? (
          <>
            <div className={`banner ${compare.verdict === 'regressed' ? 'red' : compare.verdict === 'improved' ? 'green' : 'amber'}`} style={{ marginBottom: 12 }}>
              <strong>
                {lang === 'zh' ? '回归结论' : 'Verdict'}: {compare.verdict.toUpperCase()} — {filters.platform.toUpperCase()} v{compare.baseline_version} → v{compare.candidate_version}
              </strong>
              <span>{compare.regressed_count} {lang === 'zh' ? '项劣化' : 'regressed'} · {compare.gated_count} {lang === 'zh' ? '项受门禁保护' : 'gated'}</span>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{lang === 'zh' ? '指标' : 'Metric'}</th>
                    <th>v{compare.baseline_version}</th>
                    <th>v{compare.candidate_version}</th>
                    <th>{lang === 'zh' ? '变化幅度' : 'Delta'}</th>
                    <th>{lang === 'zh' ? '统计检验与 p-value' : 'Statistical Test'}</th>
                    <th>{lang === 'zh' ? '判定' : 'Status'}</th>
                  </tr>
                </thead>
                <tbody>
                  {compare.metrics.map(m => (
                    <tr key={m.metric_id}>
                      <td><strong>{metricLabel(m.metric_id)}</strong><div className="mono kpi-meta">{m.metric_id}</div></td>
                      <td>
                        {formatValue(m.baseline_value, m.unit)}
                        <div className="kpi-meta">n={m.baseline_sample_size.toLocaleString('en-US')}</div>
                      </td>
                      <td>
                        {formatValue(m.candidate_value, m.unit)}
                        <div className="kpi-meta">n={m.candidate_sample_size.toLocaleString('en-US')}</div>
                      </td>
                      <td className="mono">{formatDelta(m.absolute_delta, m.unit, m.relative_pct)}</td>
                      <td>
                        <div className="mono">{statTestLabel(m.stat_test)}</div>
                        <div className="kpi-meta">
                          {formatPValue(m.p_value)}
                          {m.statistically_significant !== undefined && (m.statistically_significant ? ' · sig' : ' · n.s.')}
                        </div>
                      </td>
                      <td>
                        <span className={`badge ${m.status === 'regressed' ? 'red' : m.status === 'improved' ? 'green' : m.status === 'gated' ? 'amber' : 'slate'}`}>
                          {m.status}
                        </span>
                        {m.gate_reason && <div className="kpi-meta">{missingLabel(m.gate_reason)}</div>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        ) : (
          <div className="empty">{lang === 'zh' ? '至少上报两个版本后可进行统计门禁回归对比。' : 'Ingest at least two releases to run comparison.'}</div>
        )}
      </div>

      {scan && scan.findings.length > 0 && (
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'scannerTitle')} (α = {scan.alpha})</h2>
              <p className="card-sub">{t(lang, 'scannerSub')}</p>
            </div>
            <span className={`badge ${scan.regressed_count > 0 ? 'red' : 'green'}`}>
              {scan.regressed_count} {lang === 'zh' ? '项显著回归' : 'significant regressions'}
            </span>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === 'zh' ? '归因范围' : 'Scope'}</th>
                  <th>{lang === 'zh' ? '指标' : 'Metric'}</th>
                  <th>{lang === 'zh' ? '变化幅度' : 'Delta'}</th>
                  <th>p-value</th>
                  <th>{lang === 'zh' ? '影响分' : 'Impact'}</th>
                  <th>{lang === 'zh' ? '状态' : 'Status'}</th>
                </tr>
              </thead>
              <tbody>
                {scan.findings.slice(0, 10).map((f, idx) => (
                  <tr key={`${f.scope}-${f.comparison.metric_id}-${idx}`}>
                    <td className="mono"><strong>{f.scope}</strong></td>
                    <td>{metricLabel(f.comparison.metric_id)}</td>
                    <td className="mono">{formatDelta(f.comparison.absolute_delta, f.comparison.unit, f.comparison.relative_pct)}</td>
                    <td className="mono">{formatPValue(f.comparison.p_value)}</td>
                    <td className="mono"><strong>{f.impact_score.toFixed(1)}</strong></td>
                    <td>
                      <span className={`badge ${f.comparison.status === 'regressed' ? 'red' : 'green'}`}>
                        {f.comparison.status}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </>
  );
}
