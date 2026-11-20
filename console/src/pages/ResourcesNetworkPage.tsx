import { useEffect, useState } from 'react';
import { api } from '../api.ts';
import { formatQuantile, formatValue } from '../format.ts';
import type { NetworkReport, Platform, ResourceSceneReport } from '../types.ts';

interface Props {
  platform: Platform;
  appVersion: string;
  refreshTick: number;
}

export function ResourcesNetworkPage({ platform, appVersion, refreshTick }: Props) {
  const [resources, setResources] = useState<ResourceSceneReport[]>([]);
  const [network, setNetwork] = useState<NetworkReport | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([
      api.resources(platform, appVersion || undefined),
      api.network(platform, appVersion || undefined),
    ])
      .then(([res, net]) => {
        if (!active) return;
        setResources(res.scenes ?? []);
        setNetwork(net);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [platform, appVersion, refreshTick]);

  const netP50 = formatQuantile(network?.duration_p50_ms, 'ms');
  const netP90 = formatQuantile(network?.duration_p90_ms, 'ms');

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Process Resources &amp; Optional RN JS LongTasks</h2>
            <p className="card-sub">Android PSS and iOS phys_footprint kept strictly distinct; RN LongTask columns show JS-adapter telemetry when attached</p>
          </div>
        </div>

        {resources.length === 0 ? (
          <div className="empty">No resource windows observed for this scope.</div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Scene</th>
                  <th>Memory Kind</th>
                  <th>Avg CPU</th>
                  <th>Peak Memory P50</th>
                  <th>Peak Memory P90</th>
                  <th>RN LongTask Blocking / Win</th>
                  <th>Max JS Task</th>
                </tr>
              </thead>
              <tbody>
                {resources.map(r => {
                  const p50 = formatQuantile(r.memory_peak_p50_mib, 'MiB');
                  const p90 = formatQuantile(r.memory_peak_p90_mib, 'MiB');
                  return (
                    <tr key={r.scene}>
                      <td><strong>{r.scene}</strong></td>
                      <td className="mono">{r.memory_kind || '—'}</td>
                      <td>{formatValue(r.avg_cpu_pct, '%', r.cpu_missing_reason)}</td>
                      <td>{p50.main}<div className="kpi-meta">{p50.sub}</div></td>
                      <td><strong>{p90.main}</strong><div className="kpi-meta">{p90.sub}</div></td>
                      <td>{formatValue(r.avg_js_blocking_ms_per_window, 'ms')}</td>
                      <td>{formatValue(r.js_max_task_ms, 'ms')}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Network Denominator &amp; Native Transport Waterfall</h2>
            <p className="card-sub">
              Logical Calls: <strong>{(network?.total_calls ?? 0).toLocaleString('en-US')}</strong> · Failed:{' '}
              <strong>{(network?.failed_calls ?? 0).toLocaleString('en-US')}</strong> · Success Rate:{' '}
              <strong>{formatValue(network?.success_rate_pct, '%', network?.missing_reason)}</strong> · P50 {netP50.main} / P90 {netP90.main}
            </p>
          </div>
        </div>

        {(network?.operations ?? []).length === 0 ? (
          <div className="empty">No native transport stage samples recorded yet.</div>
        ) : (
          <>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Operation</th>
                    <th>Samples</th>
                    <th>Success / Reuse</th>
                    <th>Stage P90 Waterfall (DNS · TCP · TLS · TTFB · Body)</th>
                    <th>Total P90</th>
                  </tr>
                </thead>
                <tbody>
                  {network!.operations.map(op => {
                    const dns = op.dns_p90_ms.value ?? 0;
                    const tcp = op.tcp_p90_ms.value ?? 0;
                    const tls = op.tls_p90_ms.value ?? 0;
                    const ttfb = op.ttfb_p90_ms.value ?? 0;
                    const body = op.body_p90_ms.value ?? 0;
                    const sum = Math.max(1, dns + tcp + tls + ttfb + body);
                    return (
                      <tr key={`${op.method}:${op.operation}`}>
                        <td><span className="badge blue">{op.method}</span> <strong className="mono">{op.operation}</strong></td>
                        <td>{op.sample_count.toLocaleString('en-US')}</td>
                        <td>
                          {formatValue(op.success_rate_pct, '%')}
                          <div className="kpi-meta">Reuse {formatValue(op.connection_reuse_rate_pct, '%')}</div>
                        </td>
                        <td>
                          <div className="bar-track">
                            <div className="bar-seg" style={{ width: `${(dns / sum) * 100}%`, background: '#0ea5e9' }} title={`DNS P90: ${dns}ms`} />
                            <div className="bar-seg" style={{ width: `${(tcp / sum) * 100}%`, background: '#6366f1' }} title={`TCP P90: ${tcp}ms`} />
                            <div className="bar-seg" style={{ width: `${(tls / sum) * 100}%`, background: '#ec4899' }} title={`TLS P90: ${tls}ms`} />
                            <div className="bar-seg" style={{ width: `${(ttfb / sum) * 100}%`, background: '#f59e0b' }} title={`TTFB P90: ${ttfb}ms`} />
                            <div className="bar-seg" style={{ width: `${(body / sum) * 100}%`, background: '#10b981' }} title={`Body P90: ${body}ms`} />
                          </div>
                          <div className="kpi-meta mono">DNS {dns} · TCP {tcp} · TLS {tls} · TTFB {ttfb} · Body {body} ms</div>
                        </td>
                        <td><strong>{formatQuantile(op.total_p90_ms, 'ms').main}</strong></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div className="legend">
              <span className="legend-item"><i className="swatch" style={{ background: '#0ea5e9' }} />DNS</span>
              <span className="legend-item"><i className="swatch" style={{ background: '#6366f1' }} />TCP</span>
              <span className="legend-item"><i className="swatch" style={{ background: '#ec4899' }} />TLS</span>
              <span className="legend-item"><i className="swatch" style={{ background: '#f59e0b' }} />TTFB</span>
              <span className="legend-item"><i className="swatch" style={{ background: '#10b981' }} />Body</span>
            </div>
          </>
        )}
      </div>
    </>
  );
}
