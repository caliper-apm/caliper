import type {
  AlertIncident,
  AlertRule,
  AuditLogEntry,
  CompareReport,
  CPUProfileRecord,
  FlamegraphReport,
  FluencySceneReport,
  HealthResponse,
  HeapCompareReport,
  HeapSnapshotRecord,
  IssueSummary,
  NetworkReport,
  OverviewReport,
  Platform,
  RegressionScanReport,
  ReleaseRecord,
  RemoteLogTask,
  ResourceSceneReport,
  SessionLookupResponse,
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

  scanRegressions: (platform: Platform, baseline: string, candidate: string) =>
    requestJson<RegressionScanReport>('/v1/analytics/regressions/scan', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        platform,
        env: 'prod',
        baseline_version: baseline,
        candidate_version: candidate,
      }),
    }),

  lookupSession: (params: { installHash?: string; userHash?: string; reason: string; platform?: Platform }, apiKey?: string) =>
    requestJson<SessionLookupResponse>(
      `/v1/sessions/lookup${query({
        install_hash: params.installHash,
        user_hash: params.userHash,
        reason: params.reason,
        platform: params.platform,
      })}`,
      { headers: apiKey ? { 'X-Caliper-Key': apiKey } : undefined },
    ),

  auditLogs: (apiKey?: string) =>
    requestJson<{ audit_logs: AuditLogEntry[] }>('/v1/audit/logs?limit=50', {
      headers: apiKey ? { 'X-Caliper-Key': apiKey } : undefined,
    }),

  cpuProfiles: (platform: Platform, appVersion?: string, runtime?: string) =>
    requestJson<{ profiles: CPUProfileRecord[] }>(
      `/v1/diagnostics/profiles${query({ platform, env: 'prod', app_version: appVersion, runtime })}`,
    ),

  flamegraph: (profileId: string) =>
    requestJson<FlamegraphReport>(`/v1/diagnostics/profiles/${encodeURIComponent(profileId)}/flamegraph`),

  heapSnapshots: (platform: Platform, appVersion?: string) =>
    requestJson<{ snapshots: HeapSnapshotRecord[] }>(
      `/v1/diagnostics/heap${query({ platform, env: 'prod', app_version: appVersion })}`,
    ),

  compareHeap: (platform: Platform, baseline: string, candidate: string) =>
    requestJson<HeapCompareReport>('/v1/diagnostics/heap/compare', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        platform,
        env: 'prod',
        baseline_version: baseline,
        candidate_version: candidate,
      }),
    }),

  remoteLogTasks: (platform: Platform) =>
    requestJson<{ tasks: RemoteLogTask[] }>(`/v1/diagnostics/log-tasks${query({ platform })}`),

  createRemoteLogTask: (
    payload: { app_id: string; platform: Platform; target_type: 'user_hash' | 'install_hash'; target_hash: string; reason: string; max_bytes?: number },
    apiKey?: string,
  ) =>
    requestJson<RemoteLogTask>('/v1/diagnostics/log-tasks', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(apiKey ? { 'X-Caliper-Key': apiKey } : {}),
      },
      body: JSON.stringify(payload),
    }),

  remoteLogContent: (taskId: string, reason: string, apiKey?: string) =>
    requestJson<{ task: RemoteLogTask; content: string }>(
      `/v1/diagnostics/log-tasks/${encodeURIComponent(taskId)}/content${query({ reason })}`,
      { headers: apiKey ? { 'X-Caliper-Key': apiKey } : undefined },
    ),

  otlpExport: (platform: Platform, appVersion?: string) =>
    requestJson<Record<string, unknown>>(
      `/v1/otlp/export${query({ platform, env: 'prod', app_version: appVersion })}`,
    ),

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
