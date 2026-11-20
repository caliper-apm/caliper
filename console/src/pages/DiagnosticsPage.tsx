import { useEffect, useMemo, useState, type FormEvent } from 'react';
import type { ECBasicOption } from 'echarts/types/dist/shared';
import { api } from '../api.ts';
import { EChart } from '../components/EChart.tsx';
import { formatTime } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type {
  CPUProfileRecord,
  FlameNode,
  FlamegraphReport,
  GlobalFilters,
  HeapCompareReport,
  ReleaseRecord,
  RemoteLogTask,
  SessionLookupResponse,
} from '../types.ts';

interface Props {
  filters: GlobalFilters;
  releases: ReleaseRecord[];
  refreshTick: number;
  lang: Lang;
  onMutated: () => void;
}

interface FlatBar {
  node: FlameNode;
  xPct: number;
  widthPct: number;
  row: number;
}

function flattenFlame(root: FlameNode): { bars: FlatBar[]; maxDepth: number } {
  const bars: FlatBar[] = [];
  let maxDepth = 0;
  const rootTotal = Math.max(root.total_samples, 1);

  function walk(node: FlameNode, xPct: number, row: number) {
    if (row > maxDepth) maxDepth = row;
    const widthPct = (node.total_samples / rootTotal) * 100;
    bars.push({ node, xPct, widthPct, row });
    let cursor = xPct;
    for (const child of node.children ?? []) {
      const childW = (child.total_samples / rootTotal) * 100;
      walk(child, cursor, row + 1);
      cursor += childW;
    }
  }
  walk(root, 0, 0);
  return { bars, maxDepth };
}

function barColor(runtime: 'native' | 'hermes', selfPct: number, depth: number): string {
  if (selfPct >= 40) return '#f53f3f';
  if (selfPct >= 20) return '#ff7d00';
  if (runtime === 'hermes') return depth % 2 === 0 ? '#722ed1' : '#551db0';
  return depth % 2 === 0 ? '#165dff' : '#0fc6c2';
}

export function DiagnosticsPage({ filters, releases, refreshTick, lang, onMutated }: Props) {
  const [profiles, setProfiles] = useState<CPUProfileRecord[]>([]);
  const [selectedProfileId, setSelectedProfileId] = useState('');
  const [flamegraph, setFlamegraph] = useState<FlamegraphReport | null>(null);
  const [zoomRoot, setZoomRoot] = useState<FlameNode | null>(null);
  const [hoveredNode, setHoveredNode] = useState<FlameNode | null>(null);

  const [heapDiff, setHeapDiff] = useState<HeapCompareReport | null>(null);

  // Audited single-user trace lookup
  const [lookupType, setLookupType] = useState<'user_hash' | 'install_hash'>('user_hash');
  const [lookupHash, setLookupHash] = useState('0000111122223333');
  const [lookupReason, setLookupReason] = useState('');
  const [lookupResult, setLookupResult] = useState<SessionLookupResponse | null>(null);

  // Remote log retrieval tasks
  const [tasks, setTasks] = useState<RemoteLogTask[]>([]);
  const [taskReason, setTaskReason] = useState('');
  const [viewedLog, setViewedLog] = useState<{ task: RemoteLogTask; content: string } | null>(null);

  const [error, setError] = useState('');
  const [statusMsg, setStatusMsg] = useState('');

  const uniqueVersions = useMemo(() => Array.from(new Set(releases.map(r => r.app_version))).sort(), [releases]);

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([api.cpuProfiles(filters), api.remoteLogTasks(filters.platform)])
      .then(([pRes, tRes]) => {
        if (!active) return;
        const pList = pRes.profiles ?? [];
        setProfiles(pList);
        setTasks(tRes.tasks ?? []);
        if (pList.length > 0) {
          // Prefer regressed v1.1.0 profile by default for immediate symbolicated demo!
          const preferred = pList.find(p => p.app_version === '1.1.0') ?? pList[0];
          setSelectedProfileId(prev => (pList.some(p => p.profile_id === prev) ? prev : preferred.profile_id));
        } else {
          setSelectedProfileId('');
          setFlamegraph(null);
        }
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [JSON.stringify(filters), refreshTick]);

  useEffect(() => {
    if (!selectedProfileId) return;
    let active = true;
    api.flamegraph(selectedProfileId)
      .then(fg => {
        if (!active) return;
        setFlamegraph(fg);
        setZoomRoot(null);
        setHoveredNode(null);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [selectedProfileId]);

  useEffect(() => {
    if (uniqueVersions.length < 2) {
      setHeapDiff(null);
      return;
    }
    let active = true;
    api.compareHeap(filters, uniqueVersions[0], uniqueVersions[1])
      .then(d => { if (active) setHeapDiff(d); })
      .catch(() => { if (active) setHeapDiff(null); });
    return () => { active = false; };
  }, [filters.platform, filters.env, filters.scene, uniqueVersions.join(','), refreshTick]);

  const heapChartOption: ECBasicOption = useMemo(() => {
    const diffs = (heapDiff?.dominator_diffs ?? []).slice(0, 8);
    return {
      tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
      legend: { top: 0, right: 10, textStyle: { fontSize: 11 } },
      grid: { top: 30, right: 24, bottom: 24, left: 150 },
      xAxis: { type: 'value', name: 'MiB', splitLine: { lineStyle: { color: '#f2f3f5' } } },
      yAxis: { type: 'category', data: diffs.map(d => d.class_name), axisLabel: { color: '#4e5969', fontSize: 11 } },
      series: [
        { name: `v${heapDiff?.baseline_version ?? ''}`, type: 'bar', barMaxWidth: 14, data: diffs.map(d => d.baseline_mib), itemStyle: { color: '#94bfff', borderRadius: [0, 3, 3, 0] } },
        {
          name: `v${heapDiff?.candidate_version ?? ''}`,
          type: 'bar',
          barMaxWidth: 14,
          data: diffs.map(d => ({
            value: d.candidate_mib,
            itemStyle: { color: d.suspect ? '#f53f3f' : '#165dff', borderRadius: [0, 3, 3, 0] },
          })),
        },
      ],
    };
  }, [heapDiff]);

  async function runSessionLookup(e: FormEvent) {
    e.preventDefault();
    setError('');
    setStatusMsg('');
    try {
      const res = await api.lookupSession({
        userHash: lookupType === 'user_hash' ? lookupHash.trim() : undefined,
        installHash: lookupType === 'install_hash' ? lookupHash.trim() : undefined,
        reason: lookupReason.trim(),
        platform: filters.platform,
      });
      setLookupResult(res);
      setStatusMsg(`${lang === 'zh' ? '已记录强审计追查日志' : 'Audited lookup recorded'}: ${res.target_type}=${res.target_hash} (${res.events.length} events)`);
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  async function createTask(e: FormEvent) {
    e.preventDefault();
    setError('');
    setStatusMsg('');
    try {
      const created = await api.createRemoteLogTask({
        app_id: releases[0]?.app_id || 'dev.caliper.demo',
        platform: filters.platform,
        target_type: lookupType,
        target_hash: lookupHash.trim(),
        reason: taskReason.trim(),
      });
      setStatusMsg(`${lang === 'zh' ? '已下发强审计远程日志任务' : 'Created remote log task'} ${created.task_id}`);
      setTaskReason('');
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  async function inspectTaskLog(task: RemoteLogTask) {
    setError('');
    try {
      const res = await api.remoteLogContent(task.task_id, `console-inspect-${task.task_id}`);
      setViewedLog(res);
    } catch (err) {
      setError(String(err));
    }
  }

  const activeRoot = zoomRoot ?? flamegraph?.root ?? null;
  const flameLayout = activeRoot ? flattenFlame(activeRoot) : null;

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}
      {statusMsg && <div className="banner green"><span>{statusMsg}</span></div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'flameTitle')}</h2>
            <p className="card-sub">
              {lang === 'zh'
                ? '自动通过构建绑定 R8 / Hermes SourceMap v3 / iOS dSYM 还原真实源码行 · 点击任意帧块缩放子树'
                : 'Auto-symbolicated via build-bound R8 / SourceMap v3 / dSYM · click any frame to zoom'}
            </p>
          </div>
          <div className="filter-group">
            <select value={selectedProfileId} onChange={e => setSelectedProfileId(e.target.value)} aria-label="CPU Profile">
              {profiles.map(p => (
                <option key={p.profile_id} value={p.profile_id}>
                  v{p.app_version} · {p.runtime.toUpperCase()} · {p.scene} ({p.total_samples} samples)
                </option>
              ))}
            </select>
            {zoomRoot && <button className="btn primary" onClick={() => setZoomRoot(null)}>{lang === 'zh' ? '重置缩放' : 'Reset Zoom'}</button>}
          </div>
        </div>

        {flamegraph && flameLayout ? (
          <>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 10, alignItems: 'center' }}>
              <span className={`badge ${flamegraph.runtime === 'hermes' ? 'purple' : 'blue'}`}>{flamegraph.runtime.toUpperCase()}</span>
              <span className={`badge ${flamegraph.symbolicated ? 'green' : 'slate'}`}>
                {flamegraph.symbolicated ? (lang === 'zh' ? '已自动符号化' : 'Auto-Symbolicated') : 'Raw'}
              </span>
              <span className="mono kpi-meta">
                scene={flamegraph.scene} · thread={flamegraph.thread_name || 'main'} · {flamegraph.sample_interval_ms}ms · {flamegraph.total_samples} samples
              </span>
            </div>

            <div style={{ marginBottom: 10, padding: '8px 12px', borderRadius: 6, background: '#141923', color: '#e5e7eb', fontSize: 12 }}>
              {hoveredNode ? (
                <span className="mono">
                  <strong style={{ color: '#6aa1ff' }}>{hoveredNode.name}</strong> — Total: <strong>{hoveredNode.total_pct.toFixed(1)}%</strong> ({hoveredNode.total_samples}) · Self: <strong style={{ color: '#ff9a2e' }}>{hoveredNode.self_pct.toFixed(1)}%</strong> ({hoveredNode.self_samples})
                </span>
              ) : (
                <span style={{ color: '#86909c' }}>
                  {lang === 'zh' ? '悬停帧块查看精确 Self / Total CPU 占比；点击帧块钻取子树。' : 'Hover a frame for self/total CPU share; click to zoom subtree.'}
                </span>
              )}
            </div>

            <div style={{ position: 'relative', height: (flameLayout.maxDepth + 1) * 30 + 10, background: '#141923', borderRadius: 6, overflow: 'hidden' }}>
              {flameLayout.bars.map((bar, idx) => (
                <button
                  key={`${bar.row}-${bar.xPct}-${idx}`}
                  type="button"
                  onClick={() => setZoomRoot(bar.node)}
                  onMouseEnter={() => setHoveredNode(bar.node)}
                  title={`${bar.node.name} | total ${bar.node.total_pct}% | self ${bar.node.self_pct}%`}
                  style={{
                    position: 'absolute',
                    left: `${bar.xPct}%`,
                    width: `calc(${Math.max(bar.widthPct, 0.8)}% - 2px)`,
                    top: bar.row * 30 + 5,
                    height: 25,
                    background: barColor(flamegraph.runtime, bar.node.self_pct, bar.row),
                    color: '#fff',
                    border: 'none',
                    borderRadius: 3,
                    padding: '0 7px',
                    fontSize: 11,
                    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                    textAlign: 'left',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    cursor: 'pointer',
                  }}
                >
                  {bar.node.name} ({bar.node.total_pct}%)
                </button>
              ))}
            </div>
          </>
        ) : (
          <div className="empty">{lang === 'zh' ? '当前筛选条件下暂无 CPU 火焰图样本。' : 'No CPU profiles in current scope.'}</div>
        )}
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'heapTitle')}</h2>
              <p className="card-sub">{lang === 'zh' ? '零 PII 类级别 Dominator 对比（绝不上传原始字符串/业务数据）' : 'Zero-PII class-level retention comparison'}</p>
            </div>
            {heapDiff && (
              <span className={`badge ${heapDiff.delta_retained_mib >= 32 ? 'red' : 'green'}`}>
                v{heapDiff.baseline_version} → v{heapDiff.candidate_version}: {heapDiff.delta_retained_mib >= 0 ? '+' : ''}{heapDiff.delta_retained_mib} MiB
              </span>
            )}
          </div>
          {heapDiff && heapDiff.dominator_diffs.length > 0 ? <EChart option={heapChartOption} height={225} /> : <div className="empty">—</div>}
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{lang === 'zh' ? 'Dominator 泄漏嫌疑明细' : 'Leak Suspect Breakdown'}</h2>
              <p className="card-sub">{lang === 'zh' ? '单类内存净增 ≥ +16 MiB 自动标记 LEAK SUSPECT' : 'Classes growing ≥ +16 MiB flagged as LEAK SUSPECT'}</p>
            </div>
          </div>
          <div className="table-wrap">
            <table>
              <thead><tr><th>Class</th><th>Baseline</th><th>Candidate</th><th>Delta</th><th>Status</th></tr></thead>
              <tbody>
                {(heapDiff?.dominator_diffs ?? []).map(d => (
                  <tr key={d.class_name}>
                    <td className="mono"><strong>{d.class_name}</strong></td>
                    <td className="mono">{d.baseline_mib.toFixed(1)} MiB</td>
                    <td className="mono">{d.candidate_mib.toFixed(1)} MiB</td>
                    <td className="mono"><strong>{d.delta_mib >= 0 ? '+' : ''}{d.delta_mib.toFixed(1)} MiB</strong></td>
                    <td><span className={`badge ${d.suspect ? 'red' : 'slate'}`}>{d.suspect ? 'LEAK SUSPECT' : 'normal'}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'uidTitle')}</h2>
              <p className="card-sub">{lang === 'zh' ? '仅接受非 PII 哈希 · 强制工单原因 · Fail-Closed 写入安全审计日志' : 'Hex hash only · mandatory ticket reason · fail-closed audit log'}</p>
            </div>
            <span className="badge amber">RBAC + Audit</span>
          </div>
          <form onSubmit={runSessionLookup} style={{ marginBottom: 12 }}>
            <div className="form-grid">
              <label className="field">{lang === 'zh' ? '标识类型' : 'Target Type'}
                <select value={lookupType} onChange={e => setLookupType(e.target.value as 'user_hash' | 'install_hash')}>
                  <option value="user_hash">user_hash</option>
                  <option value="install_hash">install_hash</option>
                </select>
              </label>
              <label className="field">{lang === 'zh' ? '十六进制哈希' : 'Hex Hash'}
                <input value={lookupHash} onChange={e => setLookupHash(e.target.value)} required />
              </label>
              <label className="field">{lang === 'zh' ? '审计原因 / 工单号 (必填)' : 'Audit Reason / Ticket'}
                <input value={lookupReason} onChange={e => setLookupReason(e.target.value)} placeholder="INC-1042 VIP 卡顿排查" required />
              </label>
            </div>
            <div style={{ marginTop: 10 }}>
              <button className="btn primary" type="submit">{lang === 'zh' ? '执行强审计单点追查' : 'Run Audited Lookup'}</button>
            </div>
          </form>
          {lookupResult && (
            <div className="table-wrap" style={{ maxHeight: 240, overflowY: 'auto' }}>
              <table>
                <thead><tr><th>{lang === 'zh' ? '时间' : 'Time'}</th><th>{lang === 'zh' ? '类型' : 'Kind'}</th><th>{lang === 'zh' ? '场景' : 'Scene'}</th><th>{lang === 'zh' ? '摘要' : 'Summary'}</th></tr></thead>
                <tbody>
                  {lookupResult.events.slice(0, 30).map(ev => (
                    <tr key={ev.event_id}>
                      <td className="mono">{formatTime(ev.occurred_at_ms).slice(5, 19)}</td>
                      <td><span className="badge blue">{ev.kind}</span></td>
                      <td className="mono">{ev.scene || '—'}</td>
                      <td className="mono">{ev.summary}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'rlogTitle')}</h2>
              <p className="card-sub">{lang === 'zh' ? '按需向目标设备下发有界环形日志拉取指令（TTL + 字节上限保护）' : 'Bounded on-demand diagnostic ring-buffer retrieval'}</p>
            </div>
            <span className="badge blue">{tasks.length} {lang === 'zh' ? '个任务' : 'tasks'}</span>
          </div>
          <form onSubmit={createTask} style={{ marginBottom: 12 }}>
            <div className="form-grid">
              <label className="field">{lang === 'zh' ? '目标哈希' : 'Target Hash'}
                <input value={lookupHash} onChange={e => setLookupHash(e.target.value)} required />
              </label>
              <label className="field">{lang === 'zh' ? '拉取原因 / 工单号' : 'Audit Reason'}
                <input value={taskReason} onChange={e => setTaskReason(e.target.value)} placeholder="INC-2048 捞取卡顿现场日志" required />
              </label>
            </div>
            <div style={{ marginTop: 10 }}>
              <button className="btn primary" type="submit">{lang === 'zh' ? '下发远程日志拉取任务' : 'Create Pull Task'}</button>
            </div>
          </form>
          <div className="table-wrap">
            <table>
              <thead><tr><th>Task ID</th><th>{lang === 'zh' ? '状态' : 'Status'}</th><th>{lang === 'zh' ? '原因' : 'Reason'}</th><th>{lang === 'zh' ? '操作' : 'Action'}</th></tr></thead>
              <tbody>
                {tasks.map(tk => (
                  <tr key={tk.task_id}>
                    <td className="mono">{tk.task_id}</td>
                    <td><span className={`badge ${tk.status === 'completed' ? 'green' : tk.status === 'pending' ? 'amber' : 'slate'}`}>{tk.status}</span></td>
                    <td>{tk.reason}</td>
                    <td>
                      {tk.status === 'completed'
                        ? <button className="btn" onClick={() => inspectTaskLog(tk)}>{lang === 'zh' ? '查看日志' : 'View'}</button>
                        : <span className="kpi-meta">{lang === 'zh' ? '等待 SDK 轮询' : 'Pending poll'}</span>}
                    </td>
                  </tr>
                ))}
                {tasks.length === 0 && <tr><td colSpan={4} className="empty">{lang === 'zh' ? '暂无远程日志任务。' : 'No remote log tasks.'}</td></tr>}
              </tbody>
            </table>
          </div>
          {viewedLog && (
            <div style={{ marginTop: 10 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
                <strong className="mono">{viewedLog.task.task_id}</strong>
                <button className="btn" onClick={() => setViewedLog(null)}>{lang === 'zh' ? '关闭' : 'Close'}</button>
              </div>
              <pre className="stack-box">{viewedLog.content || '(empty)'}</pre>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
