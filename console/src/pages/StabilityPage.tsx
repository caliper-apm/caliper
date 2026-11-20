import { useEffect, useState } from 'react';
import { api } from '../api.ts';
import { formatTime } from '../format.ts';
import type { IssueSummary, Platform } from '../types.ts';

interface Props {
  platform: Platform;
  appVersion: string;
  refreshTick: number;
}

export function StabilityPage({ platform, appVersion, refreshTick }: Props) {
  const [issues, setIssues] = useState<IssueSummary[]>([]);
  const [selected, setSelected] = useState<IssueSummary | null>(null);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    api.issues(platform, appVersion || undefined)
      .then(res => { if (active) setIssues(res.issues ?? []); })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [platform, appVersion, refreshTick]);

  function openIssue(fp: string) {
    setLoadingDetail(true);
    api.issueDetail(fp, platform)
      .then(detail => setSelected(detail))
      .catch(err => setError(String(err)))
      .finally(() => setLoadingDetail(false));
  }

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Stability Issues (Crash · ANR · Abnormal Exit)</h2>
            <p className="card-sub">Click any issue to inspect build-bound auto-symbolicated stack frames &amp; breadcrumbs</p>
          </div>
          <span className="badge blue">{issues.length} issues</span>
        </div>

        {issues.length === 0 ? (
          <div className="empty">No stability issues observed for the selected scope.</div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Type</th>
                  <th>Issue Title &amp; Fingerprint</th>
                  <th>Events</th>
                  <th>Affected Installs</th>
                  <th>Releases</th>
                  <th>Last Seen</th>
                </tr>
              </thead>
              <tbody>
                {issues.map(item => (
                  <tr key={item.fingerprint} className="clickable" onClick={() => openIssue(item.fingerprint)}>
                    <td>
                      <span className={`badge ${item.fatal ? 'red' : 'amber'}`}>{item.kind}</span>
                    </td>
                    <td>
                      <strong>{item.title}</strong>
                      <div className="mono" style={{ color: 'var(--muted)' }}>{item.fingerprint}</div>
                    </td>
                    <td><strong>{item.event_count.toLocaleString('en-US')}</strong></td>
                    <td>{item.affected_installs.toLocaleString('en-US')}</td>
                    <td className="mono">{item.releases.join(', ')}</td>
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
                <span className={`badge ${selected.fatal ? 'red' : 'amber'}`}>{selected.kind}</span>{' '}
                <span className={`badge ${selected.symbol_status === 'symbolicated' ? 'green' : selected.symbol_status === 'partial' ? 'amber' : 'slate'}`}>
                  {selected.symbol_status ?? 'unknown'}
                </span>
                <h2 className="card-title" style={{ marginTop: 8 }}>{selected.title}</h2>
                <div className="mono" style={{ color: 'var(--muted)' }}>{selected.fingerprint}</div>
              </div>
              <button className="btn" onClick={() => setSelected(null)}>Close</button>
            </div>

            {loadingDetail && <div className="kpi-meta">Resolving symbols…</div>}

            <div className="kpi-grid" style={{ gridTemplateColumns: 'repeat(3, 1fr)' }}>
              <div className="card"><div className="kpi-label">Events</div><div className="kpi-value">{selected.event_count}</div></div>
              <div className="card"><div className="kpi-label">Installs</div><div className="kpi-value">{selected.affected_installs}</div></div>
              <div className="card"><div className="kpi-label">Resolved Frames</div><div className="kpi-value">{selected.resolved_frames ?? 0}/{selected.sample_frames?.length ?? 0}</div></div>
            </div>

            {selected.matched_symbols && selected.matched_symbols.length > 0 && (
              <div className="kpi-meta">Matched Symbols: <span className="mono">{selected.matched_symbols.join(', ')}</span></div>
            )}

            <div>
              <h3 className="card-title" style={{ marginBottom: 8 }}>Stack Trace (Build-Bound Auto-Symbolication)</h3>
              <div className="stack-box">
                {(selected.sample_frames ?? []).map((f, i) => (
                  <div key={i} className={f.in_app ? 'stack-inapp' : ''}>
                    #{i} {f.module ? `[${f.module}] ` : ''}{f.symbol}
                    {f.file ? <span className="stack-file"> ({f.file}{f.line ? `:${f.line}` : ''})</span> : f.address ? ` [${f.address}]` : ''}
                  </div>
                ))}
              </div>
            </div>

            <div>
              <h3 className="card-title" style={{ marginBottom: 8 }}>Pre-Fault Breadcrumbs</h3>
              {selected.sample_breadcrumbs && selected.sample_breadcrumbs.length > 0 ? (
                <table>
                  <thead><tr><th>Time (UTC)</th><th>Category</th><th>Message</th></tr></thead>
                  <tbody>
                    {selected.sample_breadcrumbs.map((b, i) => (
                      <tr key={i}>
                        <td className="mono">{formatTime(b.timestamp_ms)}</td>
                        <td><span className="badge slate">{b.category}</span></td>
                        <td>{b.message}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <div className="kpi-meta">No breadcrumbs attached to this sample.</div>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );
}
