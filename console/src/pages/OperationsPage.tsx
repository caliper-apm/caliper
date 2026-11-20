import { useEffect, useState, type FormEvent } from 'react';
import { api } from '../api.ts';
import { formatTime, formatValue } from '../format.ts';
import { t, type Lang } from '../i18n.ts';
import type { AlertIncident, AlertRule, AuditLogEntry, GlobalFilters, ReleaseRecord, SymbolRecord } from '../types.ts';

interface Props {
  filters: GlobalFilters;
  releases: ReleaseRecord[];
  refreshTick: number;
  lang: Lang;
  onMutated: () => void;
}

export function OperationsPage({ filters, releases, refreshTick, lang, onMutated }: Props) {
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

  useEffect(() => {
    let active = true;
    setError('');
    Promise.all([
      api.symbols(filters.platform),
      api.alertRules(filters.platform),
      api.alertIncidents(),
      api.auditLogs(apiKey || undefined).catch(() => ({ audit_logs: [] as AuditLogEntry[] })),
    ])
      .then(([symRes, ruleRes, incRes, auditRes]) => {
        if (!active) return;
        setSymbols(symRes.symbols ?? []);
        setRules(ruleRes.rules ?? []);
        setIncidents(incRes.incidents ?? []);
        setAuditLogs(auditRes.audit_logs ?? []);
      })
      .catch(err => { if (active) setError(String(err)); });
    return () => { active = false; };
  }, [filters.platform, refreshTick]);

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
                <tr><th>{lang === 'zh' ? '版本 / Build' : 'Release'}</th><th>Git SHA</th><th>{lang === 'zh' ? '绑定符号' : 'Symbol'}</th><th>{lang === 'zh' ? '事件数' : 'Events'}</th></tr>
              </thead>
              <tbody>
                {releases.map(r => (
                  <tr key={`${r.app_version}-${r.build_number}`}>
                    <td><strong>v{r.app_version}</strong> <span className="mono">({r.build_number})</span></td>
                    <td className="mono">{r.git_sha || '—'}</td>
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
                <option value="fluency_jank_rate_pct">fluency_jank_rate_pct</option>
                <option value="resource_cpu_avg_pct">resource_cpu_avg_pct</option>
                <option value="resource_memory_peak_p90_mib">resource_memory_peak_p90_mib</option>
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
            <thead><tr><th>{lang === 'zh' ? '规则' : 'Rule'}</th><th>{lang === 'zh' ? '触发条件' : 'Condition'}</th><th>{lang === 'zh' ? '通道' : 'Channel'}</th><th>{lang === 'zh' ? '最新告警状态与投递回执' : 'Latest Incident & Delivery'}</th></tr></thead>
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
                      ) : <span className="kpi-meta">{lang === 'zh' ? '未触发' : 'Idle'}</span>}
                    </td>
                  </tr>
                );
              })}
              {rules.length === 0 && <tr><td colSpan={4} className="empty">{lang === 'zh' ? '暂未配置告警规则。' : 'No alert rules configured.'}</td></tr>}
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
