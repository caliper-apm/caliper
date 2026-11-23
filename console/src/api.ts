import type {
  AlertIncident,
  AlertRule,
  AuditLogEntry,
  CompareReport,
  CPUProfileRecord,
  CustomJourneyReport,
  DailyTrendPoint,
  DimensionSlice,
  FlamegraphReport,
  FluencySceneReport,
  GlobalFilters,
  HealthResponse,
  HeapCompareReport,
  HeapSnapshotRecord,
  IssueState,
  IssueStatus,
  IssueSummary,
  NetworkReport,
  NetworkTransportSample,
  OverviewReport,
  Platform,
  RegressionScanReport,
  ReleaseGateReport,
  ReleaseRecord,
  RemoteLogTask,
  ResourceSceneReport,
  SamplingPolicy,
  SessionLookupResponse,
  SourceHealthReport,
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

function filterQuery(f: GlobalFilters, extra?: Record<string, string | undefined>): string {
  return query({
    platform: f.platform,
    env: f.env,
    app_version: f.appVersion || undefined,
    adapter: f.adapter || undefined,
    scene: f.scene || undefined,
    start_day_utc: f.startDayUtc,
    end_day_utc: f.endDayUtc,
    ...extra,
  });
}

export const api = {
  health: () => requestJson<HealthResponse>('/healthz'),

  releases: (platform: Platform, env = 'prod') =>
    requestJson<{ releases: ReleaseRecord[] }>(`/v1/releases${query({ platform, env })}`),

  symbols: (platform: Platform) =>
    requestJson<{ symbols: SymbolRecord[] }>(`/v1/symbols${query({ platform })}`),

  overview: (f: GlobalFilters) =>
    requestJson<OverviewReport>(`/v1/analytics/overview${filterQuery(f)}`),

  timeseries: (f: GlobalFilters) =>
    requestJson<{ points: DailyTrendPoint[] }>(`/v1/analytics/timeseries${filterQuery(f)}`),

  breakdown: (f: GlobalFilters, dimension: 'version' | 'adapter' | 'scene' | 'device' | 'os') =>
    requestJson<{ dimension: string; slices: DimensionSlice[] }>(
      `/v1/analytics/breakdown${filterQuery(
        {
          ...f,
          appVersion: dimension === 'version' ? '' : f.appVersion,
          adapter: dimension === 'adapter' ? '' : f.adapter,
          scene: dimension === 'scene' ? '' : f.scene,
        },
        { dimension },
      )}`,
    ),

  sourceHealth: (f: GlobalFilters) =>
    requestJson<SourceHealthReport>(`/v1/ops/source-health${filterQuery(f)}`),

  compare: (f: GlobalFilters, baseline: string, candidate: string) =>
    requestJson<CompareReport>('/v1/analytics/compare', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        platform: f.platform,
        env: f.env,
        baseline_version: baseline,
        candidate_version: candidate,
        scene: f.scene || undefined,
        start_day_utc: f.startDayUtc,
        end_day_utc: f.endDayUtc,
      }),
    }),

  scanRegressions: (f: GlobalFilters, baseline: string, candidate: string) =>
    requestJson<RegressionScanReport>('/v1/analytics/regressions/scan', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        platform: f.platform,
        env: f.env,
        baseline_version: baseline,
        candidate_version: candidate,
        start_day_utc: f.startDayUtc,
        end_day_utc: f.endDayUtc,
      }),
    }),

  releaseGate: (f: GlobalFilters, baseline: string, candidate: string, requireSymbolBound = false) =>
    requestJson<ReleaseGateReport>('/v1/analytics/release-gate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        platform: f.platform,
        env: f.env,
        baseline_version: baseline,
        candidate_version: candidate,
        scene: f.scene || undefined,
        start_day_utc: f.startDayUtc,
        end_day_utc: f.endDayUtc,
        require_symbol_bound: requireSymbolBound,
      }),
    }),

  startup: (f: GlobalFilters) =>
    requestJson<StartupReport>(`/v1/analytics/startup${filterQuery(f)}`),

  fluency: (f: GlobalFilters) =>
    requestJson<{ scenes: FluencySceneReport[] }>(`/v1/analytics/fluency${filterQuery(f)}`),

  resources: (f: GlobalFilters) =>
    requestJson<{ scenes: ResourceSceneReport[] }>(`/v1/analytics/resources${filterQuery(f)}`),

  network: (f: GlobalFilters) =>
    requestJson<NetworkReport>(`/v1/analytics/network${filterQuery(f)}`),

  networkSamples: (f: GlobalFilters, operation?: string, onlyIssues = false, limit = 50) =>
    requestJson<{ samples: NetworkTransportSample[] }>(
      `/v1/analytics/network/samples${filterQuery(f, {
        operation: operation || undefined,
        only_issues: onlyIssues ? 'true' : undefined,
        limit: String(limit),
      })}`,
    ),

  customJourneys: (f: GlobalFilters) =>
    requestJson<{ journeys: CustomJourneyReport[] }>(`/v1/analytics/journeys${filterQuery(f)}`),

  issues: (f: GlobalFilters) =>
    requestJson<{ issues: IssueSummary[] }>(`/v1/stability/issues${filterQuery(f)}`),

  issueDetail: (fingerprint: string, f: GlobalFilters) =>
    requestJson<IssueSummary>(`/v1/stability/issues/${encodeURIComponent(fingerprint)}${filterQuery(f)}`),

  updateIssueState: (
    fingerprint: string,
    payload: { status: IssueStatus; assignee?: string; note?: string; resolved_in_version?: string; app_id?: string; platform?: Platform },
    apiKey?: string,
  ) =>
    requestJson<IssueState>(`/v1/stability/issues/${encodeURIComponent(fingerprint)}`, {
      method: 'PATCH',
      headers: {
        'Content-Type': 'application/json',
        ...(apiKey ? { 'X-Caliper-Key': apiKey } : {}),
      },
      body: JSON.stringify(payload),
    }),

  policy: (appId: string, platform: Platform, env = 'prod') =>
    requestJson<SamplingPolicy>(`/v1/policy${query({ app_id: appId, platform, env })}`),

  upsertPolicy: (policy: SamplingPolicy, apiKey?: string) =>
    requestJson<SamplingPolicy>('/v1/policy', {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        ...(apiKey ? { 'X-Caliper-Key': apiKey } : {}),
      },
      body: JSON.stringify(policy),
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

  cpuProfiles: (f: GlobalFilters, runtime?: string) =>
    requestJson<{ profiles: CPUProfileRecord[] }>(
      `/v1/diagnostics/profiles${filterQuery(f, { runtime })}`,
    ),

  flamegraph: (profileId: string) =>
    requestJson<FlamegraphReport>(`/v1/diagnostics/profiles/${encodeURIComponent(profileId)}/flamegraph`),

  heapSnapshots: (f: GlobalFilters) =>
    requestJson<{ snapshots: HeapSnapshotRecord[] }>(
      `/v1/diagnostics/heap${filterQuery(f)}`,
    ),

  compareHeap: (f: GlobalFilters, baseline: string, candidate: string) =>
    requestJson<HeapCompareReport>('/v1/diagnostics/heap/compare', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        platform: f.platform,
        env: f.env,
        baseline_version: baseline,
        candidate_version: candidate,
        scene: f.scene || undefined,
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

  otlpExport: (f: GlobalFilters) =>
    requestJson<Record<string, unknown>>(`/v1/otlp/export${filterQuery(f)}`),

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

  deleteAlertRule: (ruleId: string, apiKey?: string) =>
    requestJson<{ status: string; rule_id: string }>(`/v1/alerts/rules/${encodeURIComponent(ruleId)}`, {
      method: 'DELETE',
      headers: apiKey ? { 'X-Caliper-Key': apiKey } : undefined,
    }),

  testAlertWebhook: (ruleId: string, apiKey?: string) =>
    requestJson<{ rule_id: string; format: string; receipt: { delivery_status: string; delivery_http_status?: number; delivery_error?: string } }>(
      `/v1/alerts/rules/${encodeURIComponent(ruleId)}/test`,
      {
        method: 'POST',
        headers: apiKey ? { 'X-Caliper-Key': apiKey } : undefined,
      },
    ),

  evaluateAlerts: (apiKey?: string) =>
    requestJson<{ outcomes: Array<{ rule_id: string; state: string; gate_reason?: string; sample_size: number }> }>(
      '/v1/alerts/evaluate',
      {
        method: 'POST',
        headers: apiKey ? { 'X-Caliper-Key': apiKey } : undefined,
      },
    ),
};
