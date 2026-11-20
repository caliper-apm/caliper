import { useEffect, useState } from 'react';
import { api } from '../api.ts';
import { formatDelta, formatQuantile, formatValue, metricLabel, missingLabel } from '../format.ts';
import type { CompareReport, OverviewReport, Platform, ReleaseRecord } from '../types.ts';

interface Props {
  platform: Platform;
  appVersion: string;
  releases: ReleaseRecord[];
  refreshTick: number;
}

export function OverviewPage({ platform, appVersion, releases, refreshTick }: Props) {
  const [overview, setOverview] = useState<OverviewReport | null>(null);
  const [compare, setCompare] = useState<CompareReport | null>(null);
  const [error, setError] = useState('');

  const uniqueVersions = Array.from(new Set(releases.map(r => r.app_version))).sort();
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
    api.overview(platform, appVersion || undefined)
      .then(data => { if (active) setOverview(data); })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [platform, appVersion, refreshTick]);

  useEffect(() => {
    if (!baseline || !candidate) return;
    let active = true;
    api.compare(platform, baseline, candidate)
      .then(data => { if (active) setCompare(data); })
      .catch(() => { if (active) setCompare(null); });
    return () => { active = false; };
  }, [platform, baseline, candidate, refreshTick]);

  const startupP90 = formatQuantile(overview?.startup.total_p90_ms, 'ms');
  const netP90 = formatQuantile(overview?.network.duration_p90_ms, 'ms');

  const validFl = overview?.fluency.filter(f => f.valid_windows > 0) ?? [];
  const totalFrames = validFl.reduce((a, b) => a + b.total_frames, 0);
  const jankFrames = validFl.reduce((a, b) => a + b.jank_frames, 0);
  const zeroFrameWins = (overview?.fluency ?? []).reduce((a, b) => a + b.zero_frame_windows, 0);
  const overallJank = totalFrames > 0 ? (jankFrames / totalFrames) * 100 : null;

  const crashEvents = (overview?.top_issues ?? []).reduce((a, b) => a + b.event_count, 0);

  return (
    <>
      {error && <div className="banner red"><span>Failed to load overview: {error}</span></div>}

      <div className="kpi-grid">
        <div className="card">
          <div className="kpi-label">Cold Start P90</div>
          <div className="kpi-value">{startupP90.main}</div>
          <div className="kpi-meta">{startupP90.sub}</div>
        </div>
        <div className="card">
          <div className="kpi-label">Fluency Jank Rate</div>
          <div className="kpi-value">{formatValue(overallJank, '%', 'no_observations')}</div>
          <div className="kpi-meta">
            {totalFrames.toLocaleString('en-US')} frames · {zeroFrameWins} zero-frame windows excluded
          </div>
        </div>
        <div className="card">
          <div className="kpi-label">API Success Rate</div>
          <div className="kpi-value">
            {formatValue(overview?.network.success_rate_pct, '%', overview?.network.missing_reason)}
          </div>
          <div className="kpi-meta">
            {(overview?.network.total_calls ?? 0).toLocaleString('en-US')} logical calls · P90 {netP90.main}
          </div>
        </div>
        <div className="card">
          <div className="kpi-label">Tracked Stability Events</div>
          <div className="kpi-value">{crashEvents.toLocaleString('en-US')}</div>
          <div className="kpi-meta">{overview?.top_issues.length ?? 0} fingerprinted issues</div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Zero-Lie Release Regression Comparison</h2>
            <p className="card-sub">
              Mergeable histograms · sample-size gate (n ≥ 100) · strict algorithm isolation · missing never zero-filled
            </p>
          </div>
          <div className="controls">
            <label>
              Baseline{' '}
              <select value={baseline} onChange={e => setBaseline(e.target.value)}>
                {uniqueVersions.map(v => <option key={v} value={v}>v{v}</option>)}
              </select>
            </label>
            <label>
              Candidate{' '}
              <select value={candidate} onChange={e => setCandidate(e.target.value)}>
                {uniqueVersions.map(v => <option key={v} value={v}>v{v}</option>)}
              </select>
            </label>
          </div>
        </div>

        {compare ? (
          <>
            <div className={`banner ${compare.verdict === 'regressed' ? 'red' : compare.verdict === 'improved' ? 'green' : 'amber'}`} style={{ marginBottom: 14 }}>
              <strong>
                Verdict: {compare.verdict.toUpperCase()} — {platform.toUpperCase()} v{compare.baseline_version} → v{compare.candidate_version}
              </strong>
              <span>{compare.regressed_count} regressed · {compare.gated_count} gated</span>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Metric</th>
                    <th>Baseline (v{compare.baseline_version})</th>
                    <th>Candidate (v{compare.candidate_version})</th>
                    <th>Delta</th>
                    <th>Status / Gate</th>
                  </tr>
                </thead>
                <tbody>
                  {compare.metrics.map(m => (
                    <tr key={m.metric_id}>
                      <td><strong>{metricLabel(m.metric_id)}</strong><div className="mono" style={{ color: 'var(--muted)' }}>{m.metric_id}</div></td>
                      <td>
                        {formatValue(m.baseline_value, m.unit)}
                        <div className="kpi-meta">n={m.baseline_sample_size.toLocaleString('en-US')}</div>
                      </td>
                      <td>
                        {formatValue(m.candidate_value, m.unit)}
                        <div className="kpi-meta">n={m.candidate_sample_size.toLocaleString('en-US')}</div>
                      </td>
                      <td>{formatDelta(m.absolute_delta, m.unit, m.relative_pct)}</td>
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
          <div className="empty">Ingest at least two releases on this platform to run gated release comparison.</div>
        )}
      </div>
    </>
  );
}
