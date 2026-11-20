import { useEffect, useState } from 'react';
import { api } from '../api.ts';
import { formatQuantile, formatValue } from '../format.ts';
import type { FluencySceneReport, Platform, StartupReport } from '../types.ts';

interface Props {
  platform: Platform;
  appVersion: string;
  refreshTick: number;
}

export function FluencyPage({ platform, appVersion, refreshTick }: Props) {
  const [startup, setStartup] = useState<StartupReport | null>(null);
  const [scenes, setScenes] = useState<FluencySceneReport[]>([]);
  const [selectedStack, setSelectedStack] = useState<{ scene: string; stacks: string[][] } | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([
      api.startup(platform, appVersion || undefined),
      api.fluency(platform, appVersion || undefined),
    ])
      .then(([st, fl]) => {
        if (!active) return;
        setStartup(st);
        setScenes(fl.scenes ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [platform, appVersion, refreshTick]);

  const totalP50 = formatQuantile(startup?.total_p50_ms, 'ms');
  const totalP90 = formatQuantile(startup?.total_p90_ms, 'ms');
  const natVal = startup?.native_init_p90_ms.value ?? 0;
  const rtVal = startup?.runtime_init_p90_ms.value ?? 0;
  const ffVal = startup?.first_frame_p90_ms.value ?? 0;
  const sumStages = Math.max(1, natVal + rtVal + ffVal);

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">3-Stage Cold Start Breakdown ({platform.toUpperCase()})</h2>
            <p className="card-sub">
              Same-platform anchor only · stage conservation enforced · staged coverage:{' '}
              {formatValue(startup?.staged_coverage_pct, '%')}
            </p>
          </div>
          <div className="controls">
            <span className="badge blue">P50 {totalP50.main}</span>
            <span className="badge amber">P90 {totalP90.main}</span>
          </div>
        </div>

        <div className="bar-track" style={{ height: 22 }}>
          <div className="bar-seg" style={{ width: `${(natVal / sumStages) * 100}%`, background: '#2563eb' }} title={`Native Init P90: ${natVal} ms`} />
          <div className="bar-seg" style={{ width: `${(rtVal / sumStages) * 100}%`, background: '#7c3aed' }} title={`Runtime Init P90: ${rtVal} ms`} />
          <div className="bar-seg" style={{ width: `${(ffVal / sumStages) * 100}%`, background: '#0891b2' }} title={`First Frame P90: ${ffVal} ms`} />
        </div>
        <div className="legend">
          <span className="legend-item"><i className="swatch" style={{ background: '#2563eb' }} />Native Init P90: <strong>{formatQuantile(startup?.native_init_p90_ms, 'ms').main}</strong></span>
          <span className="legend-item"><i className="swatch" style={{ background: '#7c3aed' }} />Runtime / Bridge P90: <strong>{formatQuantile(startup?.runtime_init_p90_ms, 'ms').main}</strong></span>
          <span className="legend-item"><i className="swatch" style={{ background: '#0891b2' }} />First Content Frame P90: <strong>{formatQuantile(startup?.first_frame_p90_ms, 'ms').main}</strong></span>
          <span className="legend-item">Completed samples: <strong>{(startup?.success_count ?? 0).toLocaleString('en-US')}</strong></span>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Scene Fluency &amp; Same-Window Stall Stacks</h2>
            <p className="card-sub">Algorithms strictly separated · zero-frame windows preserved separately (never reported as 0% jank)</p>
          </div>
        </div>

        {scenes.length === 0 ? (
          <div className="empty">No fluency windows recorded for this scope.</div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Scene</th>
                  <th>Algorithm</th>
                  <th>Valid / Zero-Frame Windows</th>
                  <th>Total Frames</th>
                  <th>Jank Rate</th>
                  <th>Severity</th>
                  <th>Stall Stacks</th>
                </tr>
              </thead>
              <tbody>
                {scenes.map(s => (
                  <tr key={`${s.scene}-${s.algorithm}`}>
                    <td><strong>{s.scene}</strong></td>
                    <td className="mono">{s.algorithm}</td>
                    <td>{s.valid_windows.toLocaleString('en-US')} valid · <span style={{ color: 'var(--amber)' }}>{s.zero_frame_windows} zero-frame</span></td>
                    <td>{s.total_frames.toLocaleString('en-US')}</td>
                    <td><strong>{formatValue(s.jank_rate_pct, '%', s.missing_reason)}</strong></td>
                    <td>
                      <span className={`badge ${s.severity === 'critical' ? 'red' : s.severity === 'degraded' ? 'amber' : s.severity === 'healthy' ? 'green' : 'slate'}`}>
                        {s.severity}
                      </span>
                    </td>
                    <td>
                      {s.sample_stall_stacks && s.sample_stall_stacks.length > 0 ? (
                        <button className="btn" onClick={() => setSelectedStack({ scene: s.scene, stacks: s.sample_stall_stacks! })}>
                          View {s.stall_window_count} stacks
                        </button>
                      ) : (
                        <span className="kpi-meta">—</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {selectedStack && (
        <div className="drawer-backdrop" onClick={() => setSelectedStack(null)}>
          <div className="drawer" onClick={e => e.stopPropagation()}>
            <div className="card-header" style={{ marginBottom: 0 }}>
              <div>
                <h2 className="card-title">Same-Window Stall Stacks — {selectedStack.scene}</h2>
                <p className="card-sub">Automatically retraced against uploaded build symbols</p>
              </div>
              <button className="btn" onClick={() => setSelectedStack(null)}>Close</button>
            </div>
            {selectedStack.stacks.map((stack, idx) => (
              <div key={idx}>
                <div className="kpi-label" style={{ marginBottom: 6 }}>Sample Stall Window #{idx + 1}</div>
                <div className="stack-box">
                  {stack.map((line, li) => <div key={li} className="stack-inapp">#{li} {line}</div>)}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </>
  );
}
