import { useEffect, useState, type FormEvent } from 'react';
import { api } from '../api.ts';
import { formatTime, formatValue } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type { AlertIncident, AlertRule, AuditLogEntry, GlobalFilters, ReleaseRecord, SamplingPolicy, SourceHealthReport, SymbolRecord } from '../types.ts';

const ALL_COLLECTORS = ['startup', 'fluency', 'resources', 'network', 'stability', 'cpu_profile', 'heap_snapshot'] as const;

interface Props {
  filters: GlobalFilters;
  releases: ReleaseRecord[];
  refreshTick: number;
  lang: Lang;
  onMutated: () => void;
}

export function OperationsPage({ filters, releases, refreshTick, lang, onMutated }: Props) {
  const [sourceHealth, setSourceHealth] = useState<SourceHealthReport | null>(null);
  const [policy, setPolicy] = useState<SamplingPolicy | null>(null);
  const [policyPct, setPolicyPct] = useState('100');
  const [policyMax24h, setPolicyMax24h] = useState('500');
  const [policyTtlMin, setPolicyTtlMin] = useState('60');
  const [policyCollectors, setPolicyCollectors] = useState<string[]>([...ALL_COLLECTORS]);
  const [symbols, setSymbols] = useState<SymbolRecord[]>([]);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [incidents, setIncidents] = useState<AlertIncident[]>([]);
  const [auditLogs, setAuditLogs] = useState<AuditLogEntry[]>([]);
  const [otlpPreview, setOtlpPreview] = useState('');
  const [statusMsg, setStatusMsg] = useState('');
  const [error, setError] = useState('');

  const [ruleName, setRuleName] = useState('冷启动 P90 劣化告警');
  const [metricId, setMetricId] = useState('startup_cold_p90_ms');
  const [operator, setOperator] = useState<'gt' | 'lt'>('gt');
  const [threshold, setThreshold] = useState('1000');
  const [minSamples, setMinSamples] = useState('20');
  const [webhookFormat, setWebhookFormat] = useState<'generic' | 'feishu' | 'slack' | 'discord'>('feishu');
  const [webhookUrl, setWebhookUrl] = useState('');
  const [apiKey, setApiKey] = useState('');

  const activeAppId = releases[0]?.app_id || 'dev.caliper.demo';

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([
      api.sourceHealth(filters),
      api.policy(activeAppId, filters.platform, filters.env).catch(() => null),
      api.symbols(filters.platform),
      api.alertRules(filters.platform),
      api.alertIncidents(),
      api.auditLogs(apiKey || undefined).catch(() => ({ audit_logs: [] as AuditLogEntry[] })),
    ])
      .then(([healthRes, polRes, symRes, ruleRes, incRes, auditRes]) => {
        if (!active) return;
        setSourceHealth(healthRes);
        if (polRes) {
          setPolicy(polRes);
          setPolicyPct(String(Number((polRes.rate_ppm / 10000).toFixed(2))));
          setPolicyMax24h(String(polRes.max_events_24h || 500));
          setPolicyTtlMin(String(Math.max(1, Math.round((polRes.lease_ttl_ms || 3600000) / 60000))));
          setPolicyCollectors(polRes.enabled_collectors?.length ? polRes.enabled_collectors : [...ALL_COLLECTORS]);
        }
        setSymbols(symRes.symbols ?? []);
        setRules(ruleRes.rules ?? []);
        setIncidents(incRes.incidents ?? []);
        setAuditLogs(auditRes.audit_logs ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [activeAppId, filters.platform, filters.env, refreshTick]);

  function toggleCollector(c: string) {
    setPolicyCollectors(prev => (prev.includes(c) ? prev.filter(x => x !== c) : [...prev, c]));
  }

  async function savePolicy(e: FormEvent) {
    e.preventDefault();
    setError('');
    setStatusMsg('');
    try {
      const pct = Math.max(0, Math.min(100, Number(policyPct) || 0));
      const saved = await api.upsertPolicy({
        policy_id: policy?.policy_id || `pol-${filters.platform}-${filters.env}`,
        app_id: activeAppId,
        platform: filters.platform,
        env: filters.env,
        rate_ppm: Math.round(pct * 10000),
        lease_ttl_ms: Math.max(60_000, Math.round((Number(policyTtlMin) || 60) * 60_000)),
        max_events_24h: Math.max(10, Math.round(Number(policyMax24h) || 500)),
        enabled_collectors: policyCollectors,
      }, apiKey || undefined);
      setPolicy(saved);
      setStatusMsg(
        lang === 'zh'
          ? `远程采样策略已生效 (${saved.policy_id} · ${(saved.rate_ppm / 10000).toFixed(1)}% · ${saved.enabled_collectors.length} 个探针)，并已写入审计日志`
          : `Published remote policy ${saved.policy_id} (${(saved.rate_ppm / 10000).toFixed(1)}%, ${saved.enabled_collectors.length} collectors)`,
      );
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  async function toggleRuleEnabled(rule: AlertRule) {
    setError('');
    setStatusMsg('');
    try {
      await api.upsertAlertRule({ ...rule, enabled: !rule.enabled }, apiKey || undefined);
      setStatusMsg(`${rule.rule_id}: ${!rule.enabled ? 'enabled' : 'disabled'}`);
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  async function removeRule(ruleId: string) {
    setError('');
    setStatusMsg('');
    try {
      await api.deleteAlertRule(ruleId, apiKey || undefined);
      setStatusMsg(`${lang === 'zh' ? '已删除告警规则' : 'Deleted alert rule'} ${ruleId}`);
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  async function sendTestWebhook(ruleId: string) {
    setError('');
    setStatusMsg('');
    try {
      const res = await api.testAlertWebhook(ruleId, apiKey || undefined);
      setStatusMsg(
        `${lang === 'zh' ? '测试 Webhook 投递回执' : 'Test webhook receipt'} [${res.format}]: ${res.receipt.delivery_status}` +
          (res.receipt.delivery_http_status ? ` HTTP ${res.receipt.delivery_http_status}` : '') +
          (res.receipt.delivery_error ? ` (${res.receipt.delivery_error})` : ''),
      );
    } catch (err) {
      setError(String(err));
    }
  }

  async function saveRule(e: FormEvent) {
    e.preventDefault();
    setError('');
    setStatusMsg('');
    try {
      const id = `rule-${filters.platform}-${metricId}-${Date.now().toString(36)}`;
      await api.upsertAlertRule({
        rule_id: id,
        name: ruleName.trim() || metricId,
        app_id: releases[0]?.app_id || 'dev.caliper.demo',
        platform: filters.platform,
        env: filters.env,
        metric_id: metricId,
        operator,
        threshold: Number(threshold),
        min_samples: Number(minSamples),
        webhook_url: webhookUrl.trim(),
        webhook_format: webhookFormat,
        cooldown_sec: 300,
        enabled: true,
      }, apiKey || undefined);
      setStatusMsg(`${lang === 'zh' ? '已保存告警规则' : 'Saved rule'} ${id}`);
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
      setStatusMsg(summary || (lang === 'zh' ? '暂无已启用的告警规则。' : 'No enabled rules.'));
      onMutated();
    } catch (err) {
      setError(String(err));
    }
  }

  async function loadOtlp() {
    setError('');
    try {
      const data = await api.otlpExport(filters);
      setOtlpPreview(JSON.stringify(data, null, 2));
    } catch (err) {
      setError(String(err));
    }
  }

  return (
    <>
      {error && <div className="banner red"><span>{error}</span></div>}
      {statusMsg && <div className="banner green"><span>{statusMsg}</span></div>}

      {sourceHealth && (
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{lang === 'zh' ? '数据源健康度与采集可信度自检 (Source Health)' : 'Data Source Health & Telemetry Integrity'}</h2>
              <p className="card-sub">
                {lang === 'zh'
                  ? '实时评估采集契约通过率、符号表覆盖率、零帧窗口隔离率 · 配套 Prometheus 自监控 GET /metrics'
                  : 'Live ingest contract acceptance, symbol coverage, zero-frame isolation · Prometheus GET /metrics'}
              </p>
            </div>
            <div className="filter-group">
              <span className={`badge ${sourceHealth.status === 'healthy' ? 'green' : sourceHealth.status === 'warning' ? 'amber' : 'red'}`}>
                {sourceHealth.status.toUpperCase()}
              </span>
              <a className="btn" href="/metrics" target="_blank" rel="noreferrer">Prometheus /metrics</a>
            </div>
          </div>
          <div className="kpi-grid-5">
            <div className="kpi-card">
              <div className="kpi-label">{lang === 'zh' ? '契约通过率' : 'Ingest Acceptance'}</div>
              <div className="kpi-value">{sourceHealth.ingest_accept_pct.toFixed(2)}%</div>
              <div className="kpi-meta">{sourceHealth.ingest_accepted.toLocaleString('en-US')} ok · {sourceHealth.ingest_rejected} rejected</div>
            </div>
            <div className="kpi-card">
              <div className="kpi-label">{lang === 'zh' ? '符号覆盖率' : 'Symbol Coverage'}</div>
              <div className="kpi-value">{formatValue(sourceHealth.symbol_coverage_pct, '%')}</div>
              <div className="kpi-meta">{sourceHealth.symbol_mapped_releases}/{sourceHealth.total_releases} {lang === 'zh' ? '版本已绑定符号' : 'releases mapped'}</div>
            </div>
            <div className="kpi-card">
              <div className="kpi-label">{lang === 'zh' ? '零帧窗口隔离占比' : 'Zero-Frame Ratio'}</div>
              <div className="kpi-value">{formatValue(sourceHealth.zero_frame_ratio_pct, '%')}</div>
              <div className="kpi-meta">{sourceHealth.fluency_zero_windows}/{sourceHealth.fluency_total_windows} windows</div>
            </div>
            <div className="kpi-card">
              <div className="kpi-label">{lang === 'zh' ? '存储引擎' : 'Storage Engine'}</div>
              <div className="mono" style={{ marginTop: 6, fontWeight: 700 }}>{sourceHealth.storage_engine}</div>
              <div className="kpi-meta">{sourceHealth.total_stored_events.toLocaleString('en-US')} events · up {sourceHealth.uptime_seconds}s</div>
            </div>
          </div>
        </div>
      )}

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{lang === 'zh' ? '动态采样与探针开关远程配置 (Dynamic Policy Control)' : 'Dynamic Sampling & Collector Policy Control'}</h2>
            <p className="card-sub">
              {lang === 'zh'
                ? '对标火山 APM Plus 远程降级与动态采样：无需发版即可按平台/环境热更新采样率、日配额与各模块探针开关 (GET/PUT /v1/policy)'
                : 'Live remote sampling rate, 24h device quota, and collector switches without shipping an app update (GET/PUT /v1/policy)'}
            </p>
          </div>
          <span className="badge blue mono">
            {policy?.policy_id || `default-${filters.platform}`} · {filters.platform}/{filters.env}
          </span>
        </div>
        <form onSubmit={savePolicy}>
          <div className="form-grid">
            <label className="field">
              {lang === 'zh' ? `全量采样率 (%) · ${Math.round((Number(policyPct) || 0) * 10000).toLocaleString('en-US')} PPM` : `Sampling Rate (%) · PPM`}
              <input type="number" min="0" max="100" step="0.5" value={policyPct} onChange={e => setPolicyPct(e.target.value)} required />
            </label>
            <label className="field">
              {lang === 'zh' ? '单设备 24h 上报上限' : 'Max Events / Device / 24h'}
              <input type="number" min="10" max="50000" value={policyMax24h} onChange={e => setPolicyMax24h(e.target.value)} required />
            </label>
            <label className="field">
              {lang === 'zh' ? '策略租约 TTL (分钟)' : 'Lease TTL (minutes)'}
              <input type="number" min="1" max="1440" value={policyTtlMin} onChange={e => setPolicyTtlMin(e.target.value)} required />
            </label>
          </div>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 10, alignItems: 'center' }}>
            <span className="kpi-label">{lang === 'zh' ? '启用探针模块：' : 'Collectors:'}</span>
            {ALL_COLLECTORS.map(col => {
              const checked = policyCollectors.includes(col);
              return (
                <label
                  key={col}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 5,
                    padding: '4px 10px',
                    borderRadius: 6,
                    border: `1px solid ${checked ? '#165dff' : 'var(--border)'}`,
                    background: checked ? '#e8f3ff' : '#fff',
                    fontSize: 12,
                    cursor: 'pointer',
                  }}
                >
                  <input type="checkbox" checked={checked} onChange={() => toggleCollector(col)} />
                  <span className="mono">{col}</span>
                </label>
              );
            })}
          </div>
          <div style={{ marginTop: 12, display: 'flex', gap: 10, alignItems: 'center' }}>
            <button className="btn primary" type="submit">{lang === 'zh' ? '下发并生效远程策略' : 'Publish Live Policy'}</button>
            <span className="kpi-meta">{lang === 'zh' ? '策略变更将写入不可篡改审计日志，客户端下次轮询 GET /v1/policy 即刻生效' : 'Audited immediately; mobile SDKs adopt on next GET /v1/policy lease refresh'}</span>
          </div>
        </form>
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'releasesTitle')}</h2>
              <p className="card-sub">{lang === 'zh' ? '首次上报或 Gradle / Fastlane 插件上传符号后自动注册' : 'Auto-discovered on ingest or CI symbol upload'}</p>
            </div>
            <span className="badge blue">{releases.length}</span>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr><th>{lang === 'zh' ? '版本 / Build' : 'Release'}</th><th>Git SHA</th><th>{lang === 'zh' ? '包体积' : 'Binary Size'}</th><th>{lang === 'zh' ? '绑定符号' : 'Symbol'}</th><th>{lang === 'zh' ? '事件数' : 'Events'}</th></tr>
              </thead>
              <tbody>
                {releases.map(r => (
                  <tr key={`${r.app_version}-${r.build_number}`}>
                    <td><strong>v{r.app_version}</strong> <span className="mono">({r.build_number})</span></td>
                    <td className="mono">{r.git_sha || '—'}</td>
                    <td className="mono">
                      {r.binary_size_bytes && r.binary_size_bytes > 0 ? (
                        <>
                          {(r.binary_size_bytes / (1024 * 1024)).toFixed(2)} MiB
                          {r.binary_artifact_kind ? <> <span className="badge slate">{r.binary_artifact_kind}</span></> : null}
                        </>
                      ) : '—'}
                    </td>
                    <td className="mono">{r.symbol_id ? <span className="badge green">{r.symbol_id}</span> : <span className="badge amber">unmapped</span>}</td>
                    <td className="mono">{r.event_count.toLocaleString('en-US')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'symbolsTitle')}</h2>
              <p className="card-sub">{lang === 'zh' ? 'SHA-256 校验落盘，支持 R8 / SourceMap v3 / dSYM / Breakpad' : 'SHA-256 verified symbol bundles'}</p>
            </div>
            <span className="badge green">{symbols.length}</span>
          </div>
          <div className="table-wrap">
            <table>
              <thead><tr><th>{lang === 'zh' ? '版本' : 'Build'}</th><th>{lang === 'zh' ? '类型' : 'Kind'}</th><th>SHA-256</th><th>{lang === 'zh' ? '大小' : 'Size'}</th></tr></thead>
              <tbody>
                {symbols.map(s => (
                  <tr key={s.symbol_id}>
                    <td className="mono">v{s.app_version} ({s.build_number})</td>
                    <td><span className="badge blue">{s.kind}</span></td>
                    <td className="mono">{s.sha256.slice(0, 14)}…</td>
                    <td className="mono">{s.size_bytes.toLocaleString('en-US')} B</td>
                  </tr>
                ))}
                {symbols.length === 0 && <tr><td colSpan={4} className="empty">—</td></tr>}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{t(lang, 'alertsTitle')}</h2>
            <p className="card-sub">{lang === 'zh' ? '样本量门禁防抖 · 冷却去重 · 原生支持飞书交互卡片 / Slack / Discord / HTTP Webhook' : 'Sample-gated evaluation · cooldown dedup · Feishu / Slack / Discord / HTTP webhooks'}</p>
          </div>
          <button className="btn primary" onClick={evaluateNow}>{lang === 'zh' ? '立即评估全部规则' : 'Evaluate All Rules'}</button>
        </div>

        <form onSubmit={saveRule} style={{ marginBottom: 14 }}>
          <div className="form-grid">
            <label className="field">{lang === 'zh' ? '规则名称' : 'Rule Name'}<input value={ruleName} onChange={e => setRuleName(e.target.value)} required /></label>
            <label className="field">{lang === 'zh' ? '监控指标' : 'Metric'}
              <select value={metricId} onChange={e => setMetricId(e.target.value)}>
                <option value="startup_cold_p90_ms">startup_cold_p90_ms</option>
                <option value="startup_cold_p95_ms">startup_cold_p95_ms</option>
                <option value="startup_cold_p99_ms">startup_cold_p99_ms</option>
                <option value="fluency_jank_rate_pct">fluency_jank_rate_pct</option>
                <option value="resource_cpu_avg_pct">resource_cpu_avg_pct</option>
                <option value="resource_memory_peak_p90_mib">resource_memory_peak_p90_mib</option>
                <option value="resource_memory_peak_p95_mib">resource_memory_peak_p95_mib</option>
                <option value="resource_memory_peak_p99_mib">resource_memory_peak_p99_mib</option>
                <option value="network_api_success_rate_pct">network_api_success_rate_pct</option>
                <option value="network_api_p90_ms">network_api_p90_ms</option>
              </select>
            </label>
            <label className="field">{lang === 'zh' ? '条件' : 'Op'}
              <select value={operator} onChange={e => setOperator(e.target.value as 'gt' | 'lt')}>
                <option value="gt">&gt; 大于</option>
                <option value="lt">&lt; 小于</option>
              </select>
            </label>
            <label className="field">{lang === 'zh' ? '阈值' : 'Threshold'}<input type="number" step="any" value={threshold} onChange={e => setThreshold(e.target.value)} required /></label>
            <label className="field">{lang === 'zh' ? '最小样本门禁' : 'Min Samples'}<input type="number" value={minSamples} onChange={e => setMinSamples(e.target.value)} required /></label>
            <label className="field">{lang === 'zh' ? '通知模板' : 'Format'}
              <select value={webhookFormat} onChange={e => setWebhookFormat(e.target.value as AlertRule['webhook_format'])}>
                <option value="feishu">飞书卡片 (Feishu)</option>
                <option value="slack">Slack Blocks</option>
                <option value="discord">Discord Embed</option>
                <option value="generic">Generic JSON</option>
              </select>
            </label>
            <label className="field">Webhook URL<input placeholder="https://..." value={webhookUrl} onChange={e => setWebhookUrl(e.target.value)} /></label>
            <label className="field">API Key (可选)<input type="password" placeholder="X-Caliper-Key" value={apiKey} onChange={e => setApiKey(e.target.value)} /></label>
          </div>
          <div style={{ marginTop: 10 }}><button className="btn" type="submit">{lang === 'zh' ? '新增告警规则' : 'Add Alert Rule'}</button></div>
        </form>

        <div className="table-wrap">
          <table>
            <thead><tr><th>{lang === 'zh' ? '规则' : 'Rule'}</th><th>{lang === 'zh' ? '触发条件' : 'Condition'}</th><th>{lang === 'zh' ? '通道' : 'Channel'}</th><th>{lang === 'zh' ? '最新告警状态与投递回执' : 'Latest Incident & Delivery'}</th><th>{lang === 'zh' ? '操作' : 'Actions'}</th></tr></thead>
            <tbody>
              {rules.map(r => {
                const inc = incidents.find(i => i.rule_id === r.rule_id);
                return (
                  <tr key={r.rule_id}>
                    <td>
                      <strong>{r.name}</strong>{' '}
                      <span className={`badge ${r.enabled ? 'green' : 'slate'}`}>{r.enabled ? (lang === 'zh' ? '启用' : 'ON') : (lang === 'zh' ? '暂停' : 'OFF')}</span>
                      <div className="mono kpi-meta">{r.rule_id}</div>
                    </td>
                    <td className="mono">{r.metric_id} {r.operator} {r.threshold} (n≥{r.min_samples})</td>
                    <td><span className="badge slate">{r.webhook_format}</span></td>
                    <td>
                      {inc ? (
                        <>
                          <span className={`badge ${inc.status === 'firing' ? 'red' : 'green'}`}>{inc.status}</span>{' '}
                          <span className="mono">obs={formatValue(inc.observed_value, '')} (n={inc.sample_size}) · {inc.delivery_status} {inc.delivery_http_status ? `HTTP ${inc.delivery_http_status}` : ''}</span>
                        </>
                      ) : <span className="kpi-meta">{lang === 'zh' ? '未触发' : 'Idle'}</span>}
                    </td>
                    <td>
                      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                        <button className="btn" onClick={() => toggleRuleEnabled(r)}>
                          {r.enabled ? (lang === 'zh' ? '暂停' : 'Pause') : (lang === 'zh' ? '启用' : 'Enable')}
                        </button>
                        {r.webhook_url && (
                          <button className="btn" onClick={() => sendTestWebhook(r.rule_id)}>
                            {lang === 'zh' ? '测通' : 'Test'}
                          </button>
                        )}
                        <button className="btn ghost" onClick={() => removeRule(r.rule_id)}>
                          {lang === 'zh' ? '删除' : 'Delete'}
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
              {rules.length === 0 && <tr><td colSpan={5} className="empty">{lang === 'zh' ? '暂未配置告警规则。' : 'No alert rules configured.'}</td></tr>}
            </tbody>
          </table>
        </div>
      </div>

      <div className="grid-2">
        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'auditTitle')}</h2>
              <p className="card-sub">{lang === 'zh' ? '记录每一次 UID 追查与远程日志拉取操作（audit.wal.jsonl 不可篡改追加）' : 'Immutable append-only security audit log'}</p>
            </div>
            <span className="badge amber">{auditLogs.length}</span>
          </div>
          <div className="table-wrap">
            <table>
              <thead><tr><th>{lang === 'zh' ? '时间' : 'Time'}</th><th>Actor</th><th>{lang === 'zh' ? '动作' : 'Action'}</th><th>{lang === 'zh' ? '工单原因' : 'Reason'}</th></tr></thead>
              <tbody>
                {auditLogs.map(a => (
                  <tr key={a.audit_id}>
                    <td className="mono">{formatTime(a.timestamp_ms).slice(5, 19)}</td>
                    <td><strong>{a.actor}</strong> <span className="badge slate">{a.role}</span></td>
                    <td className="mono">{a.action}</td>
                    <td>{a.reason}</td>
                  </tr>
                ))}
                {auditLogs.length === 0 && <tr><td colSpan={4} className="empty">{lang === 'zh' ? '暂无审计记录（执行一次单点追查或远程日志任务后将实时记录）。' : 'No audit entries yet.'}</td></tr>}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t(lang, 'otlpTitle')}</h2>
              <p className="card-sub">POST /v1/otlp/v1/traces · POST /v1/otlp/v1/logs · GET /v1/otlp/export</p>
            </div>
            <button className="btn" onClick={loadOtlp}>{lang === 'zh' ? '预览实时 OTLP 导出' : 'Preview OTLP Export'}</button>
          </div>
          {otlpPreview
            ? <pre className="stack-box" style={{ maxHeight: 240, overflow: 'auto' }}>{otlpPreview}</pre>
            : <div className="empty">{lang === 'zh' ? '点击右上角按钮导出当前筛选范围的标准 OTLP resourceMetrics JSON。' : 'Click Preview to inspect live OTLP resourceMetrics JSON.'}</div>}
        </div>
      </div>
    </>
  );
}
