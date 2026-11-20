import type {
  AlertIncident,
  AlertRule,
  CompareReport,
  FluencySceneReport,
  HealthResponse,
  IssueSummary,
  NetworkReport,
  OverviewReport,
  Platform,
  ReleaseRecord,
  ResourceSceneReport,
  StartupReport,
  SymbolRecord,
} from './types.ts';

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, init);
  const raw = await resp.text();
  if (!resp.ok) {
    throw new Error(`HTTP ${resp.status}: ${raw || resp.statusText}`);
  }
  return JSON.parse(raw) as T;
}

function query(params: Record<string, string | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v) sp.set(k, v);
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

export const api = {
  health: () => requestJson<HealthResponse>('/healthz'),

  releases: (platform: Platform) =>
    requestJson<{ releases: ReleaseRecord[] }>(`/v1/releases${query({ platform, env: 'prod' })}`),

  symbols: (platform: Platform) =>
    requestJson<{ symbols: SymbolRecord[] }>(`/v1/symbols${query({ platform })}`),

  overview: (platform: Platform, appVersion?: string) =>
    requestJson<OverviewReport>(`/v1/analytics/overview${query({ platform, env: 'prod', app_version: appVersion })}`),

  compare: (platform: Platform, baseline: string, candidate: string) =>
    requestJson<CompareReport>('/v1/analytics/compare', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        platform,
        env: 'prod',
        baseline_version: baseline,
        candidate_version: candidate,
      }),
    }),

  startup: (platform: Platform, appVersion?: string) =>
    requestJson<StartupReport>(`/v1/analytics/startup${query({ platform, env: 'prod', app_version: appVersion })}`),

  fluency: (platform: Platform, appVersion?: string) =>
    requestJson<{ scenes: FluencySceneReport[] }>(`/v1/analytics/fluency${query({ platform, env: 'prod', app_version: appVersion })}`),

  resources: (platform: Platform, appVersion?: string) =>
    requestJson<{ scenes: ResourceSceneReport[] }>(`/v1/analytics/resources${query({ platform, env: 'prod', app_version: appVersion })}`),

  network: (platform: Platform, appVersion?: string) =>
    requestJson<NetworkReport>(`/v1/analytics/network${query({ platform, env: 'prod', app_version: appVersion })}`),

  issues: (platform: Platform, appVersion?: string) =>
    requestJson<{ issues: IssueSummary[] }>(`/v1/stability/issues${query({ platform, env: 'prod', app_version: appVersion })}`),

  issueDetail: (fingerprint: string, platform: Platform) =>
    requestJson<IssueSummary>(`/v1/stability/issues/${encodeURIComponent(fingerprint)}${query({ platform })}`),

  alertRules: (platform: Platform) =>
    requestJson<{ rules: AlertRule[] }>(`/v1/alerts/rules${query({ platform })}`),

  alertIncidents: () =>
    requestJson<{ incidents: AlertIncident[] }>('/v1/alerts/incidents'),

  upsertAlertRule: (rule: AlertRule, apiKey?: string) =>
    requestJson<AlertRule>('/v1/alerts/rules', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(apiKey ? { 'X-Caliper-Key': apiKey } : {}),
      },
      body: JSON.stringify(rule),
    }),

  evaluateAlerts: (apiKey?: string) =>
    requestJson<{ outcomes: Array<{ rule_id: string; state: string; gate_reason?: string; sample_size: number }> }>(
      '/v1/alerts/evaluate',
      {
        method: 'POST',
        headers: apiKey ? { 'X-Caliper-Key': apiKey } : undefined,
      },
    ),
};
