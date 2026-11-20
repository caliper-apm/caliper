import { useEffect, useState, type FormEvent } from 'react';
import { api } from '../api.ts';
import { formatTime, formatValue } from '../format.ts';
import type { AlertIncident, AlertRule, Platform, ReleaseRecord, SymbolRecord } from '../types.ts';

interface Props {
  platform: Platform;
  releases: ReleaseRecord[];
  refreshTick: number;
  onMutated: () => void;
}

export function OperationsPage({ platform, releases, refreshTick, onMutated }: Props) {
  const [symbols, setSymbols] = useState<SymbolRecord[]>([]);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [incidents, setIncidents] = useState<AlertIncident[]>([]);
  const [statusMsg, setStatusMsg] = useState('');
  const [error, setError] = useState('');

  // Quick rule form state
  const [ruleName, setRuleName] = useState('High Cold Start P90');
  const [metricId, setMetricId] = useState('startup_cold_p90_ms');
  const [operator, setOperator] = useState<'gt' | 'lt'>('gt');
  const [threshold, setThreshold] = useState('1000');
  const [minSamples, setMinSamples] = useState('20');
  const [webhookFormat, setWebhookFormat] = useState<'generic' | 'feishu' | 'slack' | 'discord'>('feishu');
  const [webhookUrl, setWebhookUrl] = useState('');
  const [apiKey, setApiKey] = useState('');

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([
      api.symbols(platform),
      api.alertRules(platform),
      api.alertIncidents(),
    ])
      .then(([symRes, ruleRes, incRes]) => {
        if (!active) return;
        setSymbols(symRes.symbols ?? []);
        setRules(ruleRes.rules ?? []);
        setIncidents(incRes.incidents ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [platform, refreshTick]);

  async function saveRule(e: FormEvent) {
    e.preventDefault();
    setError('');
    setStatusMsg('');
    try {
      const id = `rule-${platform}-${metricId}-${Date.now().toString(36)}`;
      await api.upsertAlertRule({
        rule_id: id,
        name: ruleName.trim() || metricId,
        app_id: releases[0]?.app_id || 'dev.caliper.demo',
        platform,
        env: 'prod',
        metric_id: metricId,
        operator,
        threshold: Number(threshold),
        min_samples: Number(minSamples),
        webhook_url: webhookUrl.trim(),
        webhook_format: webhookFormat,
        cooldown_sec: 300,
        enabled: true,
      }, apiKey || undefined);
      setStatusMsg(`Saved rule ${id}`);
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  async function evaluateNow() {
    setError('');
    setStatusMsg('');
    try {
      const res = await api.evaluateAlerts(apiKey || undefined);
      const summary = res.outcomes.map(o => `${o.rule_id}: ${o.state}${o.gate_reason ? ` (${o.gate_reason})` : ''}`).join(' · ');
      setStatusMsg(summary || 'No enabled rules to evaluate.');
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}
      {statusMsg && <div className="banner green"><span>{statusMsg}</span></div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Auto-Discovered &amp; Registered Releases ({platform.toUpperCase()})</h2>
            <p className="card-sub">Builds register automatically on first ingest or CI symbol upload — zero manual version lists</p>
          </div>
          <span className="badge blue">{releases.length} releases</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th>Version / Build</th><th>Adapter</th><th>Git SHA</th><th>Bound Symbol</th><th>Events</th><th>Last Seen</th></tr>
            </thead>
            <tbody>
              {releases.map(r => (
                <tr key={`${r.app_version}-${r.build_number}`}>
                  <td><strong>v{r.app_version}</strong> <span className="mono">({r.build_number})</span></td>
                  <td><span className="badge slate">{r.adapter || 'native'}</span></td>
                  <td className="mono">{r.git_sha || '—'}</td>
                  <td className="mono">{r.symbol_id || <span className="badge amber">unmapped</span>}</td>
                  <td>{r.event_count.toLocaleString('en-US')}</td>
                  <td className="mono">{formatTime(r.last_seen_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Uploaded Symbol Bundles</h2>
            <p className="card-sub">SHA-256 verified R8 mapping, Hermes SourceMap v3, and dSYM/Breakpad tables</p>
          </div>
          <span className="badge green">{symbols.length} bundles</span>
        </div>
        {symbols.length === 0 ? (
          <div className="empty">Upload symbols via <span className="mono">caliper-symbols upload</span> to enable automatic stack resolution.</div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Symbol ID</th><th>Build</th><th>Kind</th><th>SHA-256</th><th>Size</th><th>Uploaded</th></tr></thead>
              <tbody>
                {symbols.map(s => (
                  <tr key={s.symbol_id}>
                    <td className="mono"><strong>{s.symbol_id}</strong></td>
                    <td className="mono">v{s.app_version} ({s.build_number})</td>
                    <td><span className="badge blue">{s.kind}</span></td>
                    <td className="mono">{s.sha256.slice(0, 16)}…</td>
                    <td>{s.size_bytes.toLocaleString('en-US')} B</td>
                    <td className="mono">{formatTime(s.uploaded_at_ms)}</td>
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
            <h2 className="card-title">Real Alert Rules &amp; Webhook Delivery</h2>
            <p className="card-sub">Sample-gated evaluation · cooldown dedup · live Feishu / Slack / Discord / HTTP webhooks</p>
          </div>
          <button className="btn primary" onClick={evaluateNow}>Evaluate All Rules Now</button>
        </div>

        <form onSubmit={saveRule} style={{ marginBottom: 16 }}>
          <div className="form-grid">
            <label className="field">Rule Name<input value={ruleName} onChange={e => setRuleName(e.target.value)} required /></label>
            <label className="field">Metric
              <select value={metricId} onChange={e => setMetricId(e.target.value)}>
                <option value="startup_cold_p90_ms">startup_cold_p90_ms</option>
                <option value="fluency_jank_rate_pct">fluency_jank_rate_pct</option>
                <option value="resource_cpu_avg_pct">resource_cpu_avg_pct</option>
                <option value="resource_memory_peak_p90_mib">resource_memory_peak_p90_mib</option>
                <option value="network_api_success_rate_pct">network_api_success_rate_pct</option>
                <option value="network_api_p90_ms">network_api_p90_ms</option>
              </select>
            </label>
            <label className="field">Operator
              <select value={operator} onChange={e => setOperator(e.target.value as 'gt' | 'lt')}>
                <option value="gt">&gt; greater than</option>
                <option value="lt">&lt; less than</option>
              </select>
            </label>
            <label className="field">Threshold<input type="number" step="any" value={threshold} onChange={e => setThreshold(e.target.value)} required /></label>
            <label className="field">Min Samples Gate<input type="number" value={minSamples} onChange={e => setMinSamples(e.target.value)} required /></label>
            <label className="field">Webhook Format
              <select value={webhookFormat} onChange={e => setWebhookFormat(e.target.value as AlertRule['webhook_format'])}>
                <option value="feishu">Feishu Card</option>
                <option value="slack">Slack Blocks</option>
                <option value="discord">Discord Embed</option>
                <option value="generic">Generic JSON</option>
              </select>
            </label>
            <label className="field">Webhook URL (optional)<input placeholder="https://..." value={webhookUrl} onChange={e => setWebhookUrl(e.target.value)} /></label>
            <label className="field">API Key (if server requires)<input type="password" placeholder="X-Caliper-Key" value={apiKey} onChange={e => setApiKey(e.target.value)} /></label>
          </div>
          <div style={{ marginTop: 10 }}><button className="btn" type="submit">Add Alert Rule</button></div>
        </form>

        <div className="table-wrap">
          <table>
            <thead><tr><th>Rule</th><th>Condition</th><th>Format</th><th>Latest Incident &amp; Delivery</th></tr></thead>
            <tbody>
              {rules.map(r => {
                const inc = incidents.find(i => i.rule_id === r.rule_id);
                return (
                  <tr key={r.rule_id}>
                    <td><strong>{r.name}</strong><div className="mono kpi-meta">{r.rule_id}</div></td>
                    <td className="mono">{r.metric_id} {r.operator} {r.threshold} (n≥{r.min_samples})</td>
                    <td><span className="badge slate">{r.webhook_format}</span></td>
                    <td>
                      {inc ? (
                        <>
                          <span className={`badge ${inc.status === 'firing' ? 'red' : 'green'}`}>{inc.status}</span>{' '}
                          <span className="mono">obs={formatValue(inc.observed_value, '')} (n={inc.sample_size}) · {inc.delivery_status} {inc.delivery_http_status ? `HTTP ${inc.delivery_http_status}` : ''}</span>
                        </>
                      ) : <span className="kpi-meta">Not triggered</span>}
                    </td>
                  </tr>
                );
              })}
              {rules.length === 0 && <tr><td colSpan={4} className="empty">No alert rules configured yet.</td></tr>}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}
