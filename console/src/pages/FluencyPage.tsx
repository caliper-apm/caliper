import { useEffect, useMemo, useState } from 'react';
import type { ECBasicOption } from 'echarts/types/dist/shared';
import { api } from '../api.ts';
import { EChart } from '../components/EChart.tsx';
import { formatQuantile, formatValue, missingLabel } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type { CustomJourneyReport, DailyTrendPoint, FluencySceneReport, GlobalFilters, StartupReport } from '../types.ts';

interface Props {
  filters: GlobalFilters;
  refreshTick: number;
  lang: Lang;
}

const STAGE_PALETTE = ['#165dff', '#0fc6c2', '#722ed1', '#ff7d00', '#00b42a', '#f53f3f'];

export function FluencyPage({ filters, refreshTick, lang }: Props) {
  const [startup, setStartup] = useState<StartupReport | null>(null);
  const [scenes, setScenes] = useState<FluencySceneReport[]>([]);
  const [journeys, setJourneys] = useState<CustomJourneyReport[]>([]);
  const [trends, setTrends] = useState<DailyTrendPoint[]>([]);
  const [stallDrawer, setStallDrawer] = useState<FluencySceneReport | null>(null);
  const [journeyDrawer, setJourneyDrawer] = useState<CustomJourneyReport | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([api.startup(filters), api.fluency(filters), api.customJourneys(filters), api.timeseries(filters)])
      .then(([st, fl, jrn, ts]) => {
        if (!active) return;
        setStartup(st);
        setScenes(fl.scenes ?? []);
        setJourneys(jrn.journeys ?? []);
        setTrends(ts.points ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [JSON.stringify(filters), refreshTick]);

  const nat = startup?.native_init_p90_ms.value ?? 0;
  const rt = startup?.runtime_init_p90_ms.value ?? 0;
  const ff = startup?.first_frame_p90_ms.value ?? 0;
  const stageTotal = Math.max(nat + rt + ff, 1);

  const startupHistOption: ECBasicOption = useMemo(() => {
    const buckets = (startup?.total_buckets ?? []).filter(b => b.count > 0 || (b.upper !== null && b.upper <= 5000));
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
      grid: { top: 24, right: 16, bottom: 42, left: 48 },
      xAxis: {
        type: 'category',
        data: buckets.map(b => b.label),
        axisLabel: { color: '#86909c', fontSize: 10, rotate: 28 },
      },
      yAxis: { type: 'value', name: lang === 'zh' ? '启动次数' : 'Starts', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      series: [
        {
          name: lang === 'zh' ? '样本数' : 'Samples',
          type: 'bar',
          barMaxWidth: 26,
          data: buckets.map(b => ({
            value: b.count,
            itemStyle: {
              color: b.lower >= 1500 ? '#f53f3f' : b.lower >= 750 ? '#ff7d00' : '#165dff',
              borderRadius: [3, 3, 0, 0],
            },
          })),
        },
      ],
    };
  }, [startup, lang]);

  const startupTrendOption: ECBasicOption = useMemo(() => ({
    tooltip: { trigger: 'axis', axisPointer: { type: 'cross' } },
    legend: { top: 0, right: 10, textStyle: { fontSize: 11 } },
    grid: { top: 30, right: 20, bottom: 26, left: 48 },
    xAxis: { type: 'category', data: trends.map(p => p.day_utc.slice(5)), axisLabel: { color: '#86909c', fontSize: 11 } },
    yAxis: { type: 'value', name: 'ms', splitLine: { lineStyle: { color: '#f2f3f5' } } },
    series: [
      { name: 'P90', type: 'line', smooth: 0.25, connectNulls: false, data: trends.map(p => p.startup_p90_ms), itemStyle: { color: '#165dff' }, areaStyle: { color: 'rgba(22,93,255,0.08)' } },
      { name: 'P50', type: 'line', smooth: 0.25, connectNulls: false, data: trends.map(p => p.startup_p50_ms), itemStyle: { color: '#0fc6c2' } },
    ],
  }), [trends]);

  const fluencyRankOption: ECBasicOption = useMemo(() => {
    const sorted = [...scenes].sort((a, b) => (a.jank_rate_pct ?? -1) - (b.jank_rate_pct ?? -1));
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
      grid: { top: 14, right: 38, bottom: 24, left: 120 },
      xAxis: { type: 'value', name: '%', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      yAxis: { type: 'category', data: sorted.map(s => s.scene), axisLabel: { color: '#4e5969', fontSize: 11 } },
      series: [
        {
          name: lang === 'zh' ? '卡顿率 (%)' : 'Jank Rate (%)',
          type: 'bar',
          barMaxWidth: 20,
          data: sorted.map(s => ({
            value: s.jank_rate_pct !== null ? Number(s.jank_rate_pct.toFixed(2)) : 0,
            itemStyle: {
              color: s.severity === 'critical' ? '#f53f3f' : s.severity === 'degraded' ? '#ff7d00' : '#00b42a',
              borderRadius: [0, 3, 3, 0],
            },
          })),
          label: { show: true, position: 'right', formatter: '{c}%', fontSize: 11, color: '#4e5969' },
        },
      ],
    };
  }, [scenes, lang]);

  const journeyHistOption: ECBasicOption = useMemo(() => {
    const buckets = (journeyDrawer?.duration_buckets ?? []).filter(b => b.count > 0 || (b.upper !== null && b.upper <= 3000));
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
      grid: { top: 20, right: 16, bottom: 40, left: 44 },
      xAxis: { type: 'category', data: buckets.map(b => b.label), axisLabel: { color: '#86909c', fontSize: 10, rotate: 28 } },
      yAxis: { type: 'value', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      series: [
        {
          type: 'bar',
          barMaxWidth: 22,
          data: buckets.map(b => ({
            value: b.count,
            itemStyle: {
              color: b.lower >= 1500 ? '#f53f3f' : b.lower >= 750 ? '#ff7d00' : '#165dff',
              borderRadius: [3, 3, 0, 0],
            },
          })),
        },
      ],
    };
  }, [journeyDrawer]);

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'startupStages')}</h2>
            <p className="card-sub">
              {lang === 'zh' ? '单时间锚点守恒校验 (Native Init + Runtime + First Content Frame ≤ Total)' : 'Single-anchor stage conservation (Native + Runtime + First Frame ≤ Total)'}
            </p>
          </div>
          <span className="badge blue">
            {startup?.success_count.toLocaleString('en-US') ?? 0} {lang === 'zh' ? '次成功启动' : 'starts'} · {lang === 'zh' ? '分阶段覆盖' : 'staged'} {formatValue(startup?.staged_coverage_pct, '%')}
          </span>
        </div>

        <div className="kpi-grid-5" style={{ marginBottom: 14 }}>
          <div className="kpi-card">
            <div className="kpi-label">Total P50</div>
            <div className="kpi-value">{formatQuantile(startup?.total_p50_ms, 'ms').main}</div>
            <div className="kpi-meta">{formatQuantile(startup?.total_p50_ms, 'ms').sub}</div>
          </div>
          <div className="kpi-card">
            <div className="kpi-label">Total P90</div>
            <div className="kpi-value">{formatQuantile(startup?.total_p90_ms, 'ms').main}</div>
            <div className="kpi-meta">{formatQuantile(startup?.total_p90_ms, 'ms').sub}</div>
          </div>
          <div className="kpi-card">
            <div className="kpi-label">Native Init P90</div>
            <div className="kpi-value">{formatQuantile(startup?.native_init_p90_ms, 'ms').main}</div>
            <div className="kpi-meta">{lang === 'zh' ? '进程与原生底座初始化' : 'Native shell init'}</div>
          </div>
          <div className="kpi-card">
            <div className="kpi-label">Runtime / Bridge P90</div>
            <div className="kpi-value">{formatQuantile(startup?.runtime_init_p90_ms, 'ms').main}</div>
            <div className="kpi-meta">{lang === 'zh' ? '可选 RN / JS 引擎启动' : 'Optional RN runtime'}</div>
          </div>
          <div className="kpi-card">
            <div className="kpi-label">First Content Frame P90</div>
            <div className="kpi-value">{formatQuantile(startup?.first_frame_p90_ms, 'ms').main}</div>
            <div className="kpi-meta">{lang === 'zh' ? '首帧业务内容渲染' : 'First content paint'}</div>
          </div>
        </div>

        <div className="bar-track" style={{ height: 18 }}>
          <div className="bar-seg" style={{ width: `${(nat / stageTotal) * 100}%`, background: '#165dff' }} title={`Native Init: ${nat}ms`} />
          <div className="bar-seg" style={{ width: `${(rt / stageTotal) * 100}%`, background: '#722ed1' }} title={`Runtime: ${rt}ms`} />
          <div className="bar-seg" style={{ width: `${(ff / stageTotal) * 100}%`, background: '#00b42a' }} title={`First Frame: ${ff}ms`} />
        </div>
        <div className="legend">
          <span className="legend-item"><span className="swatch" style={{ background: '#165dff' }} />Native Init ({Math.round(nat)} ms)</span>
          <span className="legend-item"><span className="swatch" style={{ background: '#722ed1' }} />Runtime / Bridge ({Math.round(rt)} ms)</span>
          <span className="legend-item"><span className="swatch" style={{ background: '#00b42a' }} />First Content Frame ({Math.round(ff)} ms)</span>
        </div>
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'startupDist')}</h2>
              <p className="card-sub">{lang === 'zh' ? '固定边界可合并直方图（橙色 >750ms，红色 >1500ms 慢启动长尾）' : 'Fixed-bound mergeable duration histogram (>1500ms highlighted red)'}</p>
            </div>
          </div>
          <EChart option={startupHistOption} height={240} />
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{lang === 'zh' ? '冷启动 14 天走势 (P50 / P90)' : 'Daily Cold Start Trend (P50 / P90)'}</h2>
              <p className="card-sub">{lang === 'zh' ? '基于真实直方图按日合并计算分位数，绝不平均日 P90' : 'Exact daily mergeable quantiles'}</p>
            </div>
          </div>
          <EChart option={startupTrendOption} height={240} />
        </div>
      </div>

      <div className="grid-3-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{lang === 'zh' ? '分场景流畅度明细（按算法严格隔离）' : 'Per-Scene Fluency Breakdown (Algorithm-Isolated)'}</h2>
              <p className="card-sub">{lang === 'zh' ? '零帧窗口永不计入流畅度分母 · 同窗主线程卡顿栈已自动符号化' : 'Zero-frame windows excluded · same-window stall stacks auto-symbolicated'}</p>
            </div>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === 'zh' ? '场景' : 'Scene'}</th>
                  <th>{lang === 'zh' ? '算法' : 'Algorithm'}</th>
                  <th>{lang === 'zh' ? '有效 / 零帧窗口' : 'Valid / Zero'}</th>
                  <th>{lang === 'zh' ? '卡顿率' : 'Jank Rate'}</th>
                  <th>Frame P90</th>
                  <th>{lang === 'zh' ? '卡顿栈' : 'Stall Stacks'}</th>
                </tr>
              </thead>
              <tbody>
                {scenes.map(s => (
                  <tr key={`${s.scene}-${s.algorithm}`}>
                    <td><strong>{s.scene}</strong></td>
                    <td className="mono">{s.algorithm}</td>
                    <td className="mono">{s.valid_windows} / <span style={{ color: 'var(--amber)' }}>{s.zero_frame_windows}</span></td>
                    <td>
                      <span className={`badge ${s.severity === 'critical' ? 'red' : s.severity === 'degraded' ? 'amber' : s.severity === 'healthy' ? 'green' : 'slate'}`}>
                        {s.jank_rate_pct !== null ? `${s.jank_rate_pct.toFixed(2)}%` : missingLabel(s.missing_reason)}
                      </span>
                    </td>
                    <td className="mono">{formatQuantile(s.frame_p90_ms, 'ms').main}</td>
                    <td>
                      {(s.sample_stall_stacks?.length ?? 0) > 0 ? (
                        <button className="btn" onClick={() => setStallDrawer(s)}>
                          {lang === 'zh' ? `查看 (${s.stall_window_count})` : `Inspect (${s.stall_window_count})`}
                        </button>
                      ) : <span className="kpi-meta">—</span>}
                    </td>
                  </tr>
                ))}
                {scenes.length === 0 && <tr><td colSpan={6} className="empty">—</td></tr>}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'fluencyChart')}</h2>
              <p className="card-sub">{lang === 'zh' ? '绿色 <3% 健康 · 橙色 3–10% 劣化 · 红色 ≥10% 严重' : 'Healthy <3% · Degraded 3-10% · Critical ≥10%'}</p>
            </div>
          </div>
          {scenes.length > 0 ? <EChart option={fluencyRankOption} height={240} /> : <div className="empty">—</div>}
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{lang === 'zh' ? '核心业务链路与自定义页面多阶段追踪 (Custom Journeys)' : 'Custom Business Journey & Page Load Traces'}</h2>
            <p className="card-sub">
              {lang === 'zh'
                ? '对标火山 APM Plus 自定义事件/页面耗时拆解 · 支持 Kotlin / Swift / RN 原生上报 custom_scene 有序子阶段 · 点击行查看分布直方图'
                : 'Multi-stage business transactions via recordCustomJourney() · click any journey for 19-bucket histogram & error breakdown'}
            </p>
          </div>
          <span className="badge purple">{journeys.length} {lang === 'zh' ? '条核心链路' : 'journeys'}</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{lang === 'zh' ? '业务链路名' : 'Journey Name'}</th>
                <th>{lang === 'zh' ? '样本数' : 'Samples'}</th>
                <th>{lang === 'zh' ? '成功率' : 'Success'}</th>
                <th>P50 / P90</th>
                <th>{lang === 'zh' ? '多阶段 P90 瀑布拆解' : 'Stage P90 Waterfall'}</th>
              </tr>
            </thead>
            <tbody>
              {journeys.map(j => {
                const stages = j.stages ?? [];
                const sumStage = Math.max(stages.reduce((acc, st) => acc + (st.p90_ms.value ?? 0), 0), 1);
                return (
                  <tr key={j.journey_name} className="clickable" onClick={() => setJourneyDrawer(j)}>
                    <td className="mono">
                      <strong>{j.journey_name}</strong>
                      <span className="kpi-meta" style={{ marginLeft: 6 }}>↗</span>
                    </td>
                    <td className="mono">{j.total_count.toLocaleString('en-US')}</td>
                    <td>
                      <span className={`badge ${(j.success_rate_pct ?? 100) < 97 ? 'red' : (j.success_rate_pct ?? 100) < 99 ? 'amber' : 'green'}`}>
                        {formatValue(j.success_rate_pct, '%', j.missing_reason)}
                      </span>
                    </td>
                    <td className="mono">
                      {formatQuantile(j.duration_p50_ms, 'ms').main} / <strong>{formatQuantile(j.duration_p90_ms, 'ms').main}</strong>
                    </td>
                    <td>
                      {stages.length > 0 ? (
                        <>
                          <div className="bar-track">
                            {stages.map((st, idx) => (
                              <div
                                key={st.name}
                                className="bar-seg"
                                style={{
                                  width: `${((st.p90_ms.value ?? 0) / sumStage) * 100}%`,
                                  background: STAGE_PALETTE[idx % STAGE_PALETTE.length],
                                }}
                                title={`${st.name}: P90 ${Math.round(st.p90_ms.value ?? 0)}ms`}
                              />
                            ))}
                          </div>
                          <div className="mono kpi-meta">
                            {stages.map(st => `${st.name} ${Math.round(st.p90_ms.value ?? 0)}ms`).join(' → ')}
                          </div>
                        </>
                      ) : <span className="kpi-meta">—</span>}
                    </td>
                  </tr>
                );
              })}
              {journeys.length === 0 && <tr><td colSpan={5} className="empty">{lang === 'zh' ? '暂无自定义业务链路样本。' : 'No custom journeys recorded.'}</td></tr>}
            </tbody>
          </table>
        </div>
      </div>

      {stallDrawer && (
        <div className="drawer-backdrop" onClick={() => setStallDrawer(null)}>
          <div className="drawer" onClick={e => e.stopPropagation()}>
            <div className="card-header" style={{ marginBottom: 0 }}>
              <div>
                <h2 className="card-title">{lang === 'zh' ? '同窗主线程卡顿栈' : 'Same-Window Stall Stacks'} · {stallDrawer.scene}</h2>
                <div className="mono kpi-meta">{stallDrawer.algorithm} · {stallDrawer.stall_window_count} stall windows</div>
              </div>
              <button className="btn" onClick={() => setStallDrawer(null)}>{lang === 'zh' ? '关闭' : 'Close'}</button>
            </div>
            {(stallDrawer.sample_stall_stacks ?? []).map((stack, i) => (
              <div key={i}>
                <div className="kpi-meta" style={{ marginBottom: 6 }}>Sample #{i + 1}</div>
                <div className="stack-box">
                  {stack.map((line, j) => (
                    <div key={j}><span style={{ color: '#6b7280' }}>#{j} </span><span className="stack-inapp">{line}</span></div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {journeyDrawer && (
        <div className="drawer-backdrop" onClick={() => setJourneyDrawer(null)}>
          <div className="drawer" onClick={e => e.stopPropagation()}>
            <div className="card-header" style={{ marginBottom: 0 }}>
              <div>
                <h2 className="card-title">{lang === 'zh' ? '业务链路耗时分布与阶段拆解' : 'Journey Distribution & Stages'}</h2>
                <div className="mono kpi-meta">{journeyDrawer.journey_name} · {journeyDrawer.total_count} samples</div>
              </div>
              <button className="btn" onClick={() => setJourneyDrawer(null)}>{lang === 'zh' ? '关闭' : 'Close'}</button>
            </div>

            <div className="kpi-grid-5">
              <div className="kpi-card"><div className="kpi-label">P50</div><div className="kpi-value">{formatQuantile(journeyDrawer.duration_p50_ms, 'ms').main}</div></div>
              <div className="kpi-card"><div className="kpi-label">P90</div><div className="kpi-value">{formatQuantile(journeyDrawer.duration_p90_ms, 'ms').main}</div></div>
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '成功率' : 'Success'}</div><div className="kpi-value">{formatValue(journeyDrawer.success_rate_pct, '%')}</div></div>
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '失败 / 超时' : 'Fail / Timeout'}</div><div className="kpi-value">{journeyDrawer.failure_count} / {journeyDrawer.timeout_count}</div></div>
            </div>

            <div>
              <div className="kpi-label" style={{ marginBottom: 4 }}>{lang === 'zh' ? '19 桶耗时直方图分布' : '19-Bucket Duration Distribution'}</div>
              <EChart option={journeyHistOption} height={210} />
            </div>

            {(journeyDrawer.stages?.length ?? 0) > 0 && (
              <div className="table-wrap">
                <table>
                  <thead><tr><th>{lang === 'zh' ? '阶段' : 'Stage'}</th><th>{lang === 'zh' ? '样本数' : 'Samples'}</th><th>P50</th><th>P90</th></tr></thead>
                  <tbody>
                    {journeyDrawer.stages!.map((st, idx) => (
                      <tr key={st.name}>
                        <td className="mono">
                          <span className="swatch" style={{ background: STAGE_PALETTE[idx % STAGE_PALETTE.length], display: 'inline-block', marginRight: 6 }} />
                          <strong>{st.name}</strong>
                        </td>
                        <td className="mono">{st.sample_count}</td>
                        <td className="mono">{formatQuantile(st.p50_ms, 'ms').main}</td>
                        <td className="mono"><strong>{formatQuantile(st.p90_ms, 'ms').main}</strong></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            {journeyDrawer.error_codes && Object.keys(journeyDrawer.error_codes).length > 0 && (
              <div>
                <div className="kpi-label" style={{ marginBottom: 6 }}>{lang === 'zh' ? '错误码分布' : 'Error Codes'}</div>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  {Object.entries(journeyDrawer.error_codes).map(([code, cnt]) => (
                    <span key={code} className="badge red mono">{code}: {cnt}</span>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </>
  );
}
