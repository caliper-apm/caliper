import { useEffect, useMemo, useState } from 'react';
import type { ECBasicOption } from 'echarts/types/dist/shared';
import { api } from '../api.ts';
import { EChart } from '../components/EChart.tsx';
import { formatTime } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type { DailyTrendPoint, GlobalFilters, IssueSummary } from '../types.ts';

interface Props {
  filters: GlobalFilters;
  refreshTick: number;
  lang: Lang;
}

export function StabilityPage({ filters, refreshTick, lang }: Props) {
  const [issues, setIssues] = useState<IssueSummary[]>([]);
  const [trends, setTrends] = useState<DailyTrendPoint[]>([]);
  const [kindFilter, setKindFilter] = useState<'all' | 'crash' | 'anr' | 'abnormal_exit'>('all');
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<IssueSummary | null>(null);
  const [drawerTab, setDrawerTab] = useState<'stack' | 'crumbs' | 'releases'>('stack');
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([api.issues(filters), api.timeseries(filters)])
      .then(([issRes, tsRes]) => {
        if (!active) return;
        setIssues(issRes.issues ?? []);
        setTrends(tsRes.points ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [JSON.stringify(filters), refreshTick]);

  const counts = useMemo(() => {
    let crash = 0, anr = 0, exit = 0, installs = 0;
    for (const i of issues) {
      installs += i.affected_installs;
      if (i.kind === 'crash') crash += i.event_count;
      else if (i.kind === 'anr') anr += i.event_count;
      else exit += i.event_count;
    }
    return { crash, anr, exit, installs };
  }, [issues]);

  const filteredIssues = useMemo(() => {
    const q = search.trim().toLowerCase();
    return issues.filter(i => {
      if (kindFilter !== 'all' && i.kind !== kindFilter) return false;
      if (q && !i.title.toLowerCase().includes(q) && !i.fingerprint.toLowerCase().includes(q)) return false;
      return true;
    });
  }, [issues, kindFilter, search]);

  const stackedTrendOption: ECBasicOption = useMemo(() => ({
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    legend: { top: 2, right: 10, textStyle: { fontSize: 11 } },
    grid: { top: 34, right: 20, bottom: 26, left: 48 },
    xAxis: { type: 'category', data: trends.map(p => p.day_utc.slice(5)), axisLabel: { color: '#86909c', fontSize: 11 } },
    yAxis: { type: 'value', name: lang === 'zh' ? '事件数' : 'Events', splitLine: { lineStyle: { color: '#f2f3f5' } } },
    series: [
      { name: 'Crash', type: 'bar', stack: 's', barMaxWidth: 26, data: trends.map(p => p.crash_events), itemStyle: { color: '#f53f3f' } },
      { name: 'ANR / Watchdog', type: 'bar', stack: 's', barMaxWidth: 26, data: trends.map(p => p.anr_events), itemStyle: { color: '#ff7d00' } },
      { name: 'Abnormal Exit (OOM/LMK)', type: 'bar', stack: 's', barMaxWidth: 26, data: trends.map(p => p.abnormal_exit_events), itemStyle: { color: '#722ed1', borderRadius: [3, 3, 0, 0] } },
    ],
  }), [trends, lang]);

  function openDetail(item: IssueSummary) {
    setSelected(item);
    setDrawerTab('stack');
    api.issueDetail(item.fingerprint, filters)
      .then(full => setSelected(full))
      .catch(() => {});
  }

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}

      <div className="kpi-grid-5">
        <div className="kpi-card">
          <div className="kpi-head"><span className="kpi-label">Crash</span><span className="badge red">Fatal</span></div>
          <div className="kpi-value">{counts.crash.toLocaleString('en-US')}</div>
          <div className="kpi-meta">{lang === 'zh' ? '原生崩溃 & JS 未捕获异常' : 'Native & JS crashes'}</div>
        </div>
        <div className="kpi-card">
          <div className="kpi-head"><span className="kpi-label">ANR / Watchdog</span><span className="badge amber">Main Thread</span></div>
          <div className="kpi-value">{counts.anr.toLocaleString('en-US')}</div>
          <div className="kpi-meta">{lang === 'zh' ? '主线程阻塞超时' : 'Main thread stalls'}</div>
        </div>
        <div className="kpi-card">
          <div className="kpi-head"><span className="kpi-label">Abnormal Exit</span><span className="badge purple">OOM / LMK</span></div>
          <div className="kpi-value">{counts.exit.toLocaleString('en-US')}</div>
          <div className="kpi-meta">{lang === 'zh' ? '系统强杀与异常退出标记' : 'ApplicationExitInfo / Watchdog'}</div>
        </div>
        <div className="kpi-card">
          <div className="kpi-head"><span className="kpi-label">{lang === 'zh' ? '受影响设备' : 'Affected Devices'}</span><span className="badge slate">Unique</span></div>
          <div className="kpi-value">{counts.installs.toLocaleString('en-US')}</div>
          <div className="kpi-meta">{issues.length} {lang === 'zh' ? '个聚类指纹' : 'fingerprints'}</div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'stabilityTrend')}</h2>
            <p className="card-sub">{lang === 'zh' ? '按日堆叠展示崩溃、ANR 与异常退出波动趋势' : 'Daily stacked counts of Crash, ANR, and Abnormal Exit events'}</p>
          </div>
        </div>
        {trends.length > 0 ? <EChart option={stackedTrendOption} height={240} /> : <div className="empty">—</div>}
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'stabilityList')}</h2>
            <p className="card-sub">{lang === 'zh' ? '服务端自动匹配构建绑定 R8 / SourceMap v3 / dSYM 符号包还原源码行' : 'Build-bound auto-symbolication on issue open'}</p>
          </div>
          <div className="filter-group">
            <div className="sub-tabs">
              {(['all', 'crash', 'anr', 'abnormal_exit'] as const).map(k => (
                <button key={k} className={kindFilter === k ? 'active' : ''} onClick={() => setKindFilter(k)}>
                  {k === 'all' ? (lang === 'zh' ? '全部' : 'All') : k.toUpperCase()}
                </button>
              ))}
            </div>
            <input
              placeholder={lang === 'zh' ? '搜索异常标题或指纹…' : 'Filter title / fingerprint…'}
              value={search}
              onChange={e => setSearch(e.target.value)}
              style={{ width: 210 }}
            />
          </div>
        </div>

        {filteredIssues.length === 0 ? (
          <div className="empty">{lang === 'zh' ? '当前筛选条件下暂无异常事件。' : 'No matching stability issues.'}</div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === 'zh' ? '类型' : 'Kind'}</th>
                  <th>{lang === 'zh' ? '问题摘要与指纹' : 'Issue & Fingerprint'}</th>
                  <th>{lang === 'zh' ? '发生次数' : 'Events'}</th>
                  <th>{lang === 'zh' ? '影响设备' : 'Devices'}</th>
                  <th>{lang === 'zh' ? '涉及版本' : 'Releases'}</th>
                  <th>{lang === 'zh' ? '最近上报' : 'Last Seen'}</th>
                </tr>
              </thead>
              <tbody>
                {filteredIssues.map(item => (
                  <tr key={item.fingerprint} className="clickable" onClick={() => openDetail(item)}>
                    <td>
                      <span className={`badge ${item.kind === 'crash' ? 'red' : item.kind === 'anr' ? 'amber' : 'purple'}`}>
                        {item.kind}
                      </span>
                    </td>
                    <td>
                      <strong>{item.title}</strong>
                      <div className="mono kpi-meta">{item.fingerprint}</div>
                    </td>
                    <td className="mono"><strong>{item.event_count.toLocaleString('en-US')}</strong></td>
                    <td className="mono">{item.affected_installs.toLocaleString('en-US')}</td>
                    <td>
                      {item.releases.slice(0, 3).map(r => (
                        <span key={r} className="badge slate" style={{ marginRight: 4 }}>{r}</span>
                      ))}
                    </td>
                    <td className="mono">{formatTime(item.last_seen_ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {selected && (
        <div className="drawer-backdrop" onClick={() => setSelected(null)}>
          <div className="drawer" onClick={e => e.stopPropagation()}>
            <div className="card-header" style={{ marginBottom: 0 }}>
              <div>
                <div style={{ display: 'flex', gap: 6, marginBottom: 6 }}>
                  <span className={`badge ${selected.kind === 'crash' ? 'red' : selected.kind === 'anr' ? 'amber' : 'purple'}`}>{selected.kind}</span>
                  {selected.symbol_status && (
                    <span className={`badge ${selected.symbol_status === 'symbolicated' ? 'green' : selected.symbol_status === 'partial' ? 'blue' : 'slate'}`}>
                      {selected.symbol_status} ({selected.resolved_frames ?? 0})
                    </span>
                  )}
                </div>
                <h2 className="card-title">{selected.title}</h2>
                <div className="mono kpi-meta">{selected.fingerprint}</div>
              </div>
              <button className="btn" onClick={() => setSelected(null)}>{lang === 'zh' ? '关闭' : 'Close'}</button>
            </div>

            <div className="kpi-grid-5">
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '发生次数' : 'Events'}</div><div className="kpi-value">{selected.event_count}</div></div>
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '影响设备' : 'Devices'}</div><div className="kpi-value">{selected.affected_installs}</div></div>
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '首次发现' : 'First Seen'}</div><div className="mono" style={{ marginTop: 6 }}>{formatTime(selected.first_seen_ms)}</div></div>
            </div>

            <div className="sub-tabs">
              <button className={drawerTab === 'stack' ? 'active' : ''} onClick={() => setDrawerTab('stack')}>
                {lang === 'zh' ? '符号化源码堆栈' : 'Symbolicated Stack'}
              </button>
              <button className={drawerTab === 'crumbs' ? 'active' : ''} onClick={() => setDrawerTab('crumbs')}>
                {lang === 'zh' ? `面包屑轨迹 (${selected.sample_breadcrumbs?.length ?? 0})` : `Breadcrumbs (${selected.sample_breadcrumbs?.length ?? 0})`}
              </button>
              <button className={drawerTab === 'releases' ? 'active' : ''} onClick={() => setDrawerTab('releases')}>
                {lang === 'zh' ? `涉及版本 (${selected.releases.length})` : `Releases (${selected.releases.length})`}
              </button>
            </div>

            {drawerTab === 'stack' && (
              <div>
                {selected.matched_symbols && selected.matched_symbols.length > 0 && (
                  <div className="kpi-meta" style={{ marginBottom: 8 }}>
                    {lang === 'zh' ? '已自动匹配符号包：' : 'Matched symbols: '}
                    <span className="mono">{selected.matched_symbols.join(', ')}</span>
                  </div>
                )}
                <div className="stack-box">
                  {(selected.sample_frames ?? []).length === 0
                    ? (selected.exit_reason ? `Exit reason: ${selected.exit_reason}` : 'No stack frames captured.')
                    : selected.sample_frames!.map((f, idx) => (
                        <div key={idx}>
                          <span style={{ color: '#6b7280' }}>#{String(idx).padStart(2, '0')} </span>
                          <span className={f.in_app ? 'stack-inapp' : ''}>{f.symbol}</span>
                          {f.file && <span className="stack-file"> ({f.file}{f.line ? `:${f.line}` : ''})</span>}
                        </div>
                      ))}
                </div>
              </div>
            )}

            {drawerTab === 'crumbs' && (
              <div className="timeline">
                {(selected.sample_breadcrumbs ?? []).length === 0 ? (
                  <div className="empty">{lang === 'zh' ? '该事件未上报面包屑。' : 'No breadcrumbs captured.'}</div>
                ) : selected.sample_breadcrumbs!.map((b, i) => (
                  <div key={i} className="timeline-item">
                    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                      <span className="badge blue">{b.category}</span>
                      <span className="mono kpi-meta">{formatTime(b.timestamp_ms)}</span>
                    </div>
                    <div style={{ marginTop: 3 }}>{b.message}</div>
                  </div>
                ))}
              </div>
            )}

            {drawerTab === 'releases' && (
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                {selected.releases.map(r => (
                  <span key={r} className="badge blue" style={{ padding: '4px 10px' }}>{r}</span>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </>
  );
}
