import { useEffect, useState, type FormEvent } from 'react';
import { api } from '../api.ts';
import { formatTime } from '../format.ts';
import type {
  CPUProfileRecord,
  FlameNode,
  FlamegraphReport,
  HeapCompareReport,
  HeapSnapshotRecord,
  Platform,
  ReleaseRecord,
  RemoteLogTask,
} from '../types.ts';

interface Props {
  platform: Platform;
  appVersion: string;
  releases: ReleaseRecord[];
  refreshTick: number;
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
  if (selfPct >= 40) return '#dc2626';
  if (selfPct >= 20) return '#d97706';
  if (runtime === 'hermes') {
    return depth % 2 === 0 ? '#7c3aed' : '#6366f1';
  }
  return depth % 2 === 0 ? '#2563eb' : '#0284c7';
}

export function DiagnosticsPage({ platform, appVersion, releases, refreshTick, onMutated }: Props) {
  const [profiles, setProfiles] = useState<CPUProfileRecord[]>([]);
  const [selectedProfileId, setSelectedProfileId] = useState('');
  const [flamegraph, setFlamegraph] = useState<FlamegraphReport | null>(null);
  const [zoomRoot, setZoomRoot] = useState<FlameNode | null>(null);
  const [hoveredNode, setHoveredNode] = useState<FlameNode | null>(null);

  const [snapshots, setSnapshots] = useState<HeapSnapshotRecord[]>([]);
  const [heapDiff, setHeapDiff] = useState<HeapCompareReport | null>(null);

  const [tasks, setTasks] = useState<RemoteLogTask[]>([]);
  const [targetType, setTargetType] = useState<'user_hash' | 'install_hash'>('user_hash');
  const [targetHash, setTargetHash] = useState('0000111122223333');
  const [reason, setReason] = useState('');
  const [viewedLog, setViewedLog] = useState<{ task: RemoteLogTask; content: string } | null>(null);

  const [otlpPreview, setOtlpPreview] = useState<string>('');
  const [error, setError] = useState('');
  const [statusMsg, setStatusMsg] = useState('');

  const uniqueVersions = Array.from(new Set(releases.map(r => r.app_version))).sort();

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([
      api.cpuProfiles(platform, appVersion || undefined),
      api.heapSnapshots(platform, appVersion || undefined),
      api.remoteLogTasks(platform),
    ])
      .then(([pRes, hRes, tRes]) => {
        if (!active) return;
        const pList = pRes.profiles ?? [];
        setProfiles(pList);
        setSnapshots(hRes.snapshots ?? []);
        setTasks(tRes.tasks ?? []);
        if (pList.length > 0) {
          setSelectedProfileId(prev => (pList.some(p => p.profile_id === prev) ? prev : pList[0].profile_id));
        } else {
          setSelectedProfileId('');
          setFlamegraph(null);
        }
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [platform, appVersion, refreshTick]);

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
    api.compareHeap(platform, uniqueVersions[0], uniqueVersions[1])
      .then(d => { if (active) setHeapDiff(d); })
      .catch(() => { if (active) setHeapDiff(null); });
    return () => { active = false; };
  }, [platform, uniqueVersions.join(','), refreshTick]);

  async function createTask(e: FormEvent) {
    e.preventDefault();
    setError('');
    setStatusMsg('');
    try {
      const created = await api.createRemoteLogTask({
        app_id: releases[0]?.app_id || 'dev.caliper.demo',
        platform,
        target_type: targetType,
        target_hash: targetHash.trim(),
        reason: reason.trim(),
      });
      setStatusMsg(`Created audited remote log pull task ${created.task_id}`);
      setReason('');
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

  async function loadOtlpExport() {
    setError('');
    try {
      const data = await api.otlpExport(platform, appVersion || undefined);
      setOtlpPreview(JSON.stringify(data, null, 2));
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
            <h2 className="card-title">Online Native &amp; Hermes CPU Flamegraphs</h2>
            <p className="card-sub">
              Build-bound auto-symbolication (R8 / Hermes SourceMap v3 / iOS dSYM) · click any frame to zoom
            </p>
          </div>
          <div className="controls">
            <select value={selectedProfileId} onChange={e => setSelectedProfileId(e.target.value)} aria-label="CPU Profile">
              {profiles.map(p => (
                <option key={p.profile_id} value={p.profile_id}>
                  v{p.app_version} · {p.runtime.toUpperCase()} · {p.scene} ({p.total_samples} samples)
                </option>
              ))}
            </select>
            {zoomRoot && <button className="btn" onClick={() => setZoomRoot(null)}>Reset Zoom</button>}
          </div>
        </div>

        {flamegraph && flameLayout ? (
          <>
            <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginBottom: 10, fontSize: 12 }}>
              <span className="badge blue">{flamegraph.runtime.toUpperCase()}</span>
              <span className={`badge ${flamegraph.symbolicated ? 'green' : 'slate'}`}>
                {flamegraph.symbolicated ? 'Auto-Symbolicated' : 'Raw Frames'}
              </span>
              <span className="mono">
                scene={flamegraph.scene} · thread={flamegraph.thread_name || 'main'} · {flamegraph.sample_interval_ms}ms interval · {flamegraph.total_samples} samples
              </span>
            </div>

            <div style={{ marginBottom: 10, padding: '8px 10px', borderRadius: 6, background: '#0b1020', border: '1px solid var(--border)', fontSize: 12 }}>
              {hoveredNode ? (
                <span className="mono">
                  <strong>{hoveredNode.name}</strong> — Total: {hoveredNode.total_pct.toFixed(1)}% ({hoveredNode.total_samples}) · Self: {hoveredNode.self_pct.toFixed(1)}% ({hoveredNode.self_samples})
                </span>
              ) : (
                <span style={{ color: 'var(--muted)' }}>Hover a frame for exact self/total CPU share; click any frame to zoom subtree.</span>
              )}
            </div>

            <div style={{ position: 'relative', height: (flameLayout.maxDepth + 1) * 28 + 8, background: '#0b1020', borderRadius: 6, border: '1px solid var(--border)', overflow: 'hidden' }}>
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
                    top: bar.row * 28 + 4,
                    height: 24,
                    background: barColor(flamegraph.runtime, bar.node.self_pct, bar.row),
                    color: '#fff',
                    border: 'none',
                    borderRadius: 3,
                    padding: '0 6px',
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
          <div className="empty">No CPU profiles ingested for this platform/version yet.</div>
        )}
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Trimmed Heap / OOM Dominator Diagnostics</h2>
            <p className="card-sub">
              Zero-PII class-level retention summary (never raw object strings) · cross-version leak suspect detection
            </p>
          </div>
          {heapDiff && (
            <span className={`badge ${heapDiff.delta_retained_mib >= 32 ? 'red' : 'green'}`}>
              v{heapDiff.baseline_version} → v{heapDiff.candidate_version}: {heapDiff.delta_retained_mib >= 0 ? '+' : ''}{heapDiff.delta_retained_mib} MiB
            </span>
          )}
        </div>

        {heapDiff && heapDiff.dominator_diffs.length > 0 ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Dominator Class</th>
                  <th>Baseline (v{heapDiff.baseline_version})</th>
                  <th>Candidate (v{heapDiff.candidate_version})</th>
                  <th>Retained Delta</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {heapDiff.dominator_diffs.map(d => (
                  <tr key={d.class_name}>
                    <td className="mono"><strong>{d.class_name}</strong></td>
                    <td className="mono">{d.baseline_mib.toFixed(1)} MiB ({d.baseline_instances} inst)</td>
                    <td className="mono">{d.candidate_mib.toFixed(1)} MiB ({d.candidate_instances} inst)</td>
                    <td className="mono">{d.delta_mib >= 0 ? '+' : ''}{d.delta_mib.toFixed(1)} MiB</td>
                    <td>
                      <span className={`badge ${d.suspect ? 'red' : 'slate'}`}>
                        {d.suspect ? 'LEAK SUSPECT' : 'normal'}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : snapshots.length > 0 ? (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Snapshot</th><th>Trigger</th><th>Retained</th><th>Top Dominator</th></tr></thead>
              <tbody>
                {snapshots.map(s => (
                  <tr key={s.snapshot_id}>
                    <td className="mono">v{s.app_version} · {s.scene}</td>
                    <td><span className={`badge ${s.trigger_reason === 'oom_warning' ? 'red' : 'amber'}`}>{s.trigger_reason}</span></td>
                    <td className="mono">{(s.total_retained_kib / 1024).toFixed(1)} MiB</td>
                    <td className="mono">{s.dominators[0]?.class_name ?? '—'} ({((s.dominators[0]?.retained_kib ?? 0) / 1024).toFixed(1)} MiB)</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="empty">No trimmed heap snapshots ingested yet.</div>
        )}
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Audited Remote Log Retrieval Tasks</h2>
            <p className="card-sub">
              Pull bounded, PII-scrubbed diagnostic ring buffers on demand · TTL-capped · fail-closed audit log
            </p>
          </div>
          <span className="badge amber">{tasks.length} tasks</span>
        </div>

        <form onSubmit={createTask} style={{ marginBottom: 14 }}>
          <div className="form-grid">
            <label className="field">Target Type
              <select value={targetType} onChange={e => setTargetType(e.target.value as 'user_hash' | 'install_hash')}>
                <option value="user_hash">user_hash</option>
                <option value="install_hash">install_hash</option>
              </select>
            </label>
            <label className="field">Target Hex Hash
              <input value={targetHash} onChange={e => setTargetHash(e.target.value)} required />
            </label>
            <label className="field">Audit Reason / Ticket ID (required)
              <input value={reason} onChange={e => setReason(e.target.value)} placeholder="INC-2048 collect ring log" required />
            </label>
          </div>
          <div style={{ marginTop: 10 }}>
            <button className="btn primary" type="submit">Create Remote Log Pull Task</button>
          </div>
        </form>

        <div className="table-wrap">
          <table>
            <thead><tr><th>Task ID</th><th>Target</th><th>Reason</th><th>Status</th><th>Uploaded</th><th>Action</th></tr></thead>
            <tbody>
              {tasks.map(t => (
                <tr key={t.task_id}>
                  <td className="mono">{t.task_id}</td>
                  <td className="mono">{t.target_type}:{t.target_hash}</td>
                  <td>{t.reason}<div className="kpi-meta">by {t.requested_by} · {formatTime(t.created_at_ms)}</div></td>
                  <td><span className={`badge ${t.status === 'completed' ? 'green' : t.status === 'pending' ? 'amber' : 'slate'}`}>{t.status}</span></td>
                  <td className="mono">{t.uploaded_bytes ? `${t.uploaded_bytes.toLocaleString('en-US')} B` : '—'}</td>
                  <td>
                    {t.status === 'completed' ? (
                      <button className="btn" onClick={() => inspectTaskLog(t)}>View Log</button>
                    ) : <span className="kpi-meta">Awaiting SDK poll</span>}
                  </td>
                </tr>
              ))}
              {tasks.length === 0 && <tr><td colSpan={6} className="empty">No remote log retrieval tasks created yet.</td></tr>}
            </tbody>
          </table>
        </div>

        {viewedLog && (
          <div style={{ marginTop: 14 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
              <strong className="mono">Remote Log: {viewedLog.task.task_id} (SHA-256: {viewedLog.task.sha256?.slice(0, 16)}…)</strong>
              <button className="btn" onClick={() => setViewedLog(null)}>Close</button>
            </div>
            <pre className="stack-box">{viewedLog.content || '(empty log)'}</pre>
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">OpenTelemetry (OTLP) Interoperability</h2>
            <p className="card-sub">
              Ingest standard OTLP/HTTP JSON at <span className="mono">POST /v1/otlp/v1/traces</span> &amp; <span className="mono">/v1/otlp/v1/logs</span> · export aggregated <span className="mono">resourceMetrics</span>
            </p>
          </div>
          <button className="btn" onClick={loadOtlpExport}>Preview Live OTLP Export</button>
        </div>
        {otlpPreview && <pre className="stack-box" style={{ maxHeight: 260, overflow: 'auto' }}>{otlpPreview}</pre>}
      </div>
    </>
  );
}
