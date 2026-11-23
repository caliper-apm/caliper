import { useEffect, useMemo, useState, type FormEvent } from 'react';
import type { ECBasicOption } from 'echarts/types/dist/shared';
import { api } from '../api.ts';
import { EChart } from '../components/EChart.tsx';
import { formatTime } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type { DailyTrendPoint, GlobalFilters, IssueStatus, IssueSummary, ReleaseRecord } from '../types.ts';

interface Props {
  filters: GlobalFilters;
  releases: ReleaseRecord[];
  refreshTick: number;
  lang: Lang;
  initialFingerprint?: string | null;
  onClearInitialFingerprint?: () => void;
  onTraceUser?: (targetType: 'user_hash' | 'install_hash', hash: string) => void;
  onMutated?: () => void;
}

function statusBadgeClass(st?: IssueStatus): string {
  if (st === 'resolved') return 'green';
  if (st === 'in_progress') return 'blue';
  if (st === 'ignored') return 'slate';
  return 'amber';
}

function statusLabel(st: IssueStatus | undefined, lang: Lang): string {
  const s = st ?? 'open';
  if (lang === 'en') return s;
  switch (s) {
    case 'in_progress': return '处理中';
    case 'resolved': return '已修复';
    case 'ignored': return '已忽略';
    default: return '待处理';
  }
}

function breakdownBarOption(data: Record<string, number> | undefined, color: string, lang: Lang): ECBasicOption {
  const entries = Object.entries(data ?? {}).sort((a, b) => a[1] - b[1]);
  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: { top: 10, right: 32, bottom: 20, left: 112 },
    xAxis: { type: 'value', splitLine: { lineStyle: { color: '#f2f3f5' } } },
    yAxis: { type: 'category', data: entries.map(([k]) => k), axisLabel: { color: '#4e5969', fontSize: 11 } },
    series: [
      {
        name: lang === 'zh' ? '事件数' : 'Events',
        type: 'bar',
        barMaxWidth: 16,
        data: entries.map(([, v]) => v),
        itemStyle: { color, borderRadius: [0, 3, 3, 0] },
        label: { show: true, position: 'right', fontSize: 11, color: '#4e5969' },
      },
    ],
  };
}

export function StabilityPage({
  filters,
  releases,
  refreshTick,
  lang,
  initialFingerprint,
  onClearInitialFingerprint,
  onTraceUser,
  onMutated,
}: Props) {
  const [issues, setIssues] = useState<IssueSummary[]>([]);
  const [trends, setTrends] = useState<DailyTrendPoint[]>([]);
  const [kindFilter, setKindFilter] = useState<'all' | 'crash' | 'anr' | 'abnormal_exit'>('all');
  const [statusFilter, setStatusFilter] = useState<'all' | IssueStatus>('all');
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<IssueSummary | null>(null);
  const [drawerTab, setDrawerTab] = useState<'stack' | 'impact' | 'crumbs' | 'releases'>('stack');
  const [triageStatus, setTriageStatus] = useState<IssueStatus>('open');
  const [triageAssignee, setTriageAssignee] = useState('');
  const [triageResolvedVer, setTriageResolvedVer] = useState('');
  const [triageNote, setTriageNote] = useState('');
  const [triageMsg, setTriageMsg] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([api.issues(filters), api.timeseries(filters)])
      .then(([issRes, tsRes]) => {
        if (!active) return;
        const list = issRes.issues ?? [];
        setIssues(list);
        setTrends(tsRes.points ?? []);
        if (initialFingerprint) {
          const hit = list.find(i => i.fingerprint === initialFingerprint);
          if (hit) {
            openDetail(hit);
          }
          onClearInitialFingerprint?.();
        }
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [JSON.stringify(filters), refreshTick, initialFingerprint]);

  const counts = useMemo(() => {
    let crash = 0, anr = 0, exit = 0, installs = 0, openCount = 0;
    for (const i of issues) {
      installs += i.affected_installs;
      if ((i.status ?? 'open') === 'open' || i.status === 'in_progress') openCount++;
      if (i.kind === 'crash') crash += i.event_count;
      else if (i.kind === 'anr') anr += i.event_count;
      else exit += i.event_count;
    }
    return { crash, anr, exit, installs, openCount };
  }, [issues]);

  const filteredIssues = useMemo(() => {
    const q = search.trim().toLowerCase();
    return issues.filter(i => {
      if (kindFilter !== 'all' && i.kind !== kindFilter) return false;
      if (statusFilter !== 'all' && (i.status ?? 'open') !== statusFilter) return false;
      if (q && !i.title.toLowerCase().includes(q) && !i.fingerprint.toLowerCase().includes(q) && !(i.assignee ?? '').toLowerCase().includes(q)) return false;
      return true;
    });
  }, [issues, kindFilter, statusFilter, search]);

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

  function hydrateTriageForm(item: IssueSummary) {
    setTriageStatus(item.status ?? 'open');
    setTriageAssignee(item.assignee ?? '');
    setTriageResolvedVer(item.resolved_in_version ?? '');
    setTriageNote(item.note ?? '');
    setTriageMsg('');
  }

  function openDetail(item: IssueSummary) {
    setSelected(item);
    setDrawerTab('stack');
    hydrateTriageForm(item);
    api.issueDetail(item.fingerprint, filters)
      .then(full => {
        setSelected(full);
        hydrateTriageForm(full);
      })
      .catch(() => {});
  }

  async function saveTriage(e: FormEvent) {
    e.preventDefault();
    if (!selected) return;
    setError('');
    setTriageMsg('');
    try {
      const saved = await api.updateIssueState(selected.fingerprint, {
        status: triageStatus,
        assignee: triageAssignee,
        resolved_in_version: triageResolvedVer,
        note: triageNote,
        app_id: releases[0]?.app_id || 'dev.caliper.demo',
        platform: filters.platform,
      });
      const updated: IssueSummary = {
        ...selected,
        status: saved.status,
        assignee: saved.assignee,
        resolved_in_version: saved.resolved_in_version,
        note: saved.note,
        state_updated_at_ms: saved.updated_at_ms,
      };
      setSelected(updated);
      setIssues(prev => prev.map(i => (i.fingerprint === updated.fingerprint ? { ...i, ...updated } : i)));
      setTriageMsg(lang === 'zh' ? '流转状态已保存并写入审计日志' : 'Issue triage state saved & audited');
      onMutated?.();
    } catch (err) {
      setError(String(err));
    }
  }

  const deviceChartOption = useMemo(
    () => breakdownBarOption(selected?.device_breakdown, '#165dff', lang),
    [selected?.device_breakdown, lang],
  );
  const osChartOption = useMemo(
    () => breakdownBarOption(selected?.os_breakdown, '#722ed1', lang),
    [selected?.os_breakdown, lang],
  );
  const releaseChartOption = useMemo(
    () => breakdownBarOption(selected?.release_breakdown, '#ff7d00', lang),
    [selected?.release_breakdown, lang],
  );

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
        <div className="kpi-card">
          <div className="kpi-head"><span className="kpi-label">{lang === 'zh' ? '待跟进问题' : 'Active Issues'}</span><span className="badge blue">Triage</span></div>
          <div className="kpi-value">{counts.openCount}</div>
          <div className="kpi-meta">{lang === 'zh' ? '待处理 / 处理中聚类' : 'Open or in-progress clusters'}</div>
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
        <div className="card-header" style={{ flexWrap: 'wrap', gap: 10 }}>
          <div>
            <h2 className="card-title">{t(lang, 'stabilityList')}</h2>
            <p className="card-sub">{lang === 'zh' ? '支持状态流转、机型/系统分布与构建绑定自动符号化还原源码行' : 'Lifecycle triage, device/OS impact, and build-bound auto-symbolication'}</p>
          </div>
          <div className="filter-group" style={{ flexWrap: 'wrap' }}>
            <div className="sub-tabs">
              {(['all', 'crash', 'anr', 'abnormal_exit'] as const).map(k => (
                <button key={k} className={kindFilter === k ? 'active' : ''} onClick={() => setKindFilter(k)}>
                  {k === 'all' ? (lang === 'zh' ? '全部类型' : 'All Kinds') : k.toUpperCase()}
                </button>
              ))}
            </div>
            <div className="sub-tabs">
              {(['all', 'open', 'in_progress', 'resolved', 'ignored'] as const).map(s => (
                <button key={s} className={statusFilter === s ? 'active' : ''} onClick={() => setStatusFilter(s)}>
                  {s === 'all' ? (lang === 'zh' ? '全部状态' : 'All Status') : statusLabel(s, lang)}
                </button>
              ))}
            </div>
            <input
              placeholder={lang === 'zh' ? '搜索异常标题、指纹或负责人…' : 'Filter title, fingerprint, assignee…'}
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
                  <th>{lang === 'zh' ? '状态 / 负责人' : 'Status / Owner'}</th>
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
                      <span className={`badge ${statusBadgeClass(item.status)}`}>{statusLabel(item.status, lang)}</span>
                      {item.assignee && <div className="mono kpi-meta">@{item.assignee}</div>}
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
                <div style={{ display: 'flex', gap: 6, marginBottom: 6, flexWrap: 'wrap' }}>
                  <span className={`badge ${selected.kind === 'crash' ? 'red' : selected.kind === 'anr' ? 'amber' : 'purple'}`}>{selected.kind}</span>
                  <span className={`badge ${statusBadgeClass(selected.status)}`}>{statusLabel(selected.status, lang)}</span>
                  {selected.symbol_status && (
                    <span className={`badge ${selected.symbol_status === 'symbolicated' ? 'green' : selected.symbol_status === 'partial' ? 'blue' : 'slate'}`}>
                      {selected.symbol_status} ({selected.resolved_frames ?? 0})
                    </span>
                  )}
                </div>
                <h2 className="card-title">{selected.title}</h2>
                <div className="mono kpi-meta">{selected.fingerprint}</div>
              </div>
              <div className="filter-group">
                {onTraceUser && (selected.sample_user_hash || selected.sample_install_hash) && (
                  <button
                    className="btn primary"
                    onClick={() => {
                      const isUser = Boolean(selected.sample_user_hash);
                      const targetHash = selected.sample_user_hash || selected.sample_install_hash || '';
                      setSelected(null);
                      onTraceUser(isUser ? 'user_hash' : 'install_hash', targetHash);
                    }}
                  >
                    {lang === 'zh' ? '单点追查样本会话 ↗' : 'Trace Sample Session ↗'}
                  </button>
                )}
                <button className="btn" onClick={() => setSelected(null)}>{lang === 'zh' ? '关闭' : 'Close'}</button>
              </div>
            </div>

            <form onSubmit={saveTriage} style={{ padding: 12, background: '#f7f8fa', borderRadius: 8, border: '1px solid var(--border)' }}>
              <div className="form-grid">
                <label className="field">{lang === 'zh' ? '流转状态' : 'Triage Status'}
                  <select value={triageStatus} onChange={e => setTriageStatus(e.target.value as IssueStatus)}>
                    <option value="open">{statusLabel('open', lang)}</option>
                    <option value="in_progress">{statusLabel('in_progress', lang)}</option>
                    <option value="resolved">{statusLabel('resolved', lang)}</option>
                    <option value="ignored">{statusLabel('ignored', lang)}</option>
                  </select>
                </label>
                <label className="field">{lang === 'zh' ? '指派负责人' : 'Assignee'}
                  <input value={triageAssignee} onChange={e => setTriageAssignee(e.target.value)} placeholder="e.g. mobile-oncall" />
                </label>
                <label className="field">{lang === 'zh' ? '修复版本' : 'Resolved In'}
                  <input value={triageResolvedVer} onChange={e => setTriageResolvedVer(e.target.value)} placeholder="e.g. 1.2.0" />
                </label>
                <label className="field">{lang === 'zh' ? '排查备注' : 'Note'}
                  <input value={triageNote} onChange={e => setTriageNote(e.target.value)} placeholder={lang === 'zh' ? '根因或修复 PR 链接' : 'Root cause or PR link'} />
                </label>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 8 }}>
                <span className="kpi-meta" style={{ color: triageMsg ? 'var(--green)' : 'var(--muted)' }}>
                  {triageMsg || (lang === 'zh' ? '状态变更会自动记录至不可篡改审计日志' : 'Triage updates are recorded in the immutable audit trail')}
                </span>
                <button className="btn primary" type="submit">{lang === 'zh' ? '保存处置状态' : 'Save Triage'}</button>
              </div>
            </form>

            <div className="kpi-grid-5">
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '发生次数' : 'Events'}</div><div className="kpi-value">{selected.event_count}</div></div>
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '影响设备' : 'Devices'}</div><div className="kpi-value">{selected.affected_installs}</div></div>
              <div className="kpi-card"><div className="kpi-label">{lang === 'zh' ? '首次发现' : 'First Seen'}</div><div className="mono" style={{ marginTop: 6 }}>{formatTime(selected.first_seen_ms)}</div></div>
            </div>

            <div className="sub-tabs">
              <button className={drawerTab === 'stack' ? 'active' : ''} onClick={() => setDrawerTab('stack')}>
                {lang === 'zh' ? '符号化源码堆栈' : 'Symbolicated Stack'}
              </button>
              <button className={drawerTab === 'impact' ? 'active' : ''} onClick={() => setDrawerTab('impact')}>
                {lang === 'zh' ? '机型 / 系统 / 版本分布' : 'Device / OS / Release Impact'}
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

            {drawerTab === 'impact' && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
                <div>
                  <div className="kpi-label" style={{ marginBottom: 4 }}>{lang === 'zh' ? '受影响机型分布 (Device Models)' : 'Affected Device Models'}</div>
                  {Object.keys(selected.device_breakdown ?? {}).length > 0
                    ? <EChart option={deviceChartOption} height={150} />
                    : <div className="empty">—</div>}
                </div>
                <div>
                  <div className="kpi-label" style={{ marginBottom: 4 }}>{lang === 'zh' ? '受影响系统版本分布 (OS Versions)' : 'Affected OS Versions'}</div>
                  {Object.keys(selected.os_breakdown ?? {}).length > 0
                    ? <EChart option={osChartOption} height={150} />
                    : <div className="empty">—</div>}
                </div>
                <div>
                  <div className="kpi-label" style={{ marginBottom: 4 }}>{lang === 'zh' ? '受影响应用版本分布 (App Releases)' : 'Affected App Releases'}</div>
                  {Object.keys(selected.release_breakdown ?? {}).length > 0
                    ? <EChart option={releaseChartOption} height={150} />
                    : <div className="empty">—</div>}
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
