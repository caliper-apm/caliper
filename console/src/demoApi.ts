import type {
  AlertIncident,
  AlertRule,
  AuditLogEntry,
  CompareReport,
  CPUProfileRecord,
  CustomJourneyReport,
  DailyTrendPoint,
  DimensionSlice,
  DimensionsResponse,
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

interface DemoSlice {
  overview: OverviewReport;
  timeseries: { points: DailyTrendPoint[] };
  startup: StartupReport;
  fluency: { scenes: FluencySceneReport[] };
  resources: { scenes: ResourceSceneReport[] };
  network: NetworkReport;
  networkSamples: { samples: NetworkTransportSample[] };
  networkSamplesIssues: { samples: NetworkTransportSample[] };
  customJourneys: { journeys: CustomJourneyReport[] };
  issues: { issues: IssueSummary[] };
  sourceHealth: SourceHealthReport;
  cpuProfiles: { profiles: CPUProfileRecord[] };
  heapSnapshots: { snapshots: HeapSnapshotRecord[] };
  otlpExport: Record<string, unknown>;
  breakdowns: Record<'version' | 'adapter' | 'scene' | 'device' | 'os', { dimension: string; slices: DimensionSlice[] }>;
}

interface DemoCompareBundle {
  compare: CompareReport;
  scanRegressions: RegressionScanReport;
  releaseGate: ReleaseGateReport;
  releaseGateSym: ReleaseGateReport;
  compareHeap?: HeapCompareReport | null;
}

interface DemoPlatformData {
  appId: string;
  dimensions: DimensionsResponse;
  releases: { releases: ReleaseRecord[] };
  symbols: { symbols: SymbolRecord[] };
  policy: SamplingPolicy | null;
  alertRules: { rules: AlertRule[] };
  remoteLogTasks: { tasks: RemoteLogTask[] };
  remoteLogContents: Record<string, { task: RemoteLogTask; content: string }>;
  slices: Record<string, DemoSlice>;
  compares: Record<string, DemoCompareBundle>;
  issueDetails: Record<string, IssueSummary>;
  flamegraphs: Record<string, FlamegraphReport>;
  sessionsByUser: Record<string, SessionLookupResponse>;
  sessionsByInstall: Record<string, SessionLookupResponse>;
}

export interface DemoSnapshot {
  meta: {
    generated_at_utc: string;
    total_events: number;
    scenario: string;
  };
  health: HealthResponse;
  alertIncidents: { incidents: AlertIncident[] };
  auditLogs: { audit_logs: AuditLogEntry[] };
  platforms: Record<Platform, DemoPlatformData>;
}

let cachedPromise: Promise<DemoSnapshot> | null = null;

// Ephemeral in-memory overlays so interactive mutations in the public GitHub Pages demo
// respond naturally without requiring a writeable backend.
const issueStateOverlay = new Map<string, IssueState>();
const policyOverlay = new Map<string, SamplingPolicy>();
const alertRulesOverlay = new Map<Platform, AlertRule[]>();
const remoteTasksOverlay = new Map<Platform, RemoteLogTask[]>();
const auditOverlay: AuditLogEntry[] = [];

function loadSnapshot(): Promise<DemoSnapshot> {
  if (!cachedPromise) {
    const base = import.meta.env.BASE_URL || '/';
    const url = `${base.endsWith('/') ? base : `${base}/`}demo-snapshot.json`;
    cachedPromise = fetch(url).then(async (resp) => {
      if (!resp.ok) {
        throw new Error(`Demo snapshot unavailable (HTTP ${resp.status})`);
      }
      return (await resp.json()) as DemoSnapshot;
    });
  }
  return cachedPromise;
}

function keyFor(appVersion = '', adapter = '', scene = ''): string {
  return `v=${appVersion}|a=${adapter}|s=${scene}`;
}

function pickSlice(p: DemoPlatformData, f: GlobalFilters): DemoSlice {
  const candidates = [
    keyFor(f.appVersion || '', f.adapter || '', f.scene || ''),
    keyFor(f.appVersion || '', '', ''),
    keyFor('', f.adapter || '', ''),
    keyFor('', '', f.scene || ''),
    keyFor('', '', ''),
  ];
  for (const k of candidates) {
    if (p.slices[k]) return p.slices[k];
  }
  return Object.values(p.slices)[0];
}

function applyIssueOverlay(iss: IssueSummary): IssueSummary {
  const ov = issueStateOverlay.get(iss.fingerprint);
  if (!ov) return iss;
  return {
    ...iss,
    status: ov.status,
    assignee: ov.assignee,
    note: ov.note,
    resolved_in_version: ov.resolved_in_version,
    state_updated_at_ms: ov.updated_at_ms,
  };
}

function pickCompare(p: DemoPlatformData, baseline: string, candidate: string): DemoCompareBundle {
  const direct = p.compares[`${baseline}->${candidate}`];
  if (direct) return direct;
  const reverse = p.compares[`${candidate}->${baseline}`];
  if (reverse) return reverse;
  return p.compares['1.0.0->1.1.0'] || Object.values(p.compares)[0];
}

function filterPointsByDate(points: DailyTrendPoint[], f: GlobalFilters): DailyTrendPoint[] {
  if (!f.startDayUtc && !f.endDayUtc) return points;
  return points.filter((pt) => {
    if (f.startDayUtc && pt.day_utc < f.startDayUtc) return false;
    if (f.endDayUtc && pt.day_utc > f.endDayUtc) return false;
    return true;
  });
}

function recordAudit(action: string, targetType: string, targetHash: string, reason: string, resultCount: number) {
  auditOverlay.unshift({
    audit_id: `demo-aud-${Date.now().toString(36)}`,
    timestamp_ms: Date.now(),
    actor: 'demo-visitor',
    role: 'demo',
    action,
    target_type: targetType,
    target_hash: targetHash,
    reason: reason || 'interactive-demo',
    result_count: resultCount,
  });
}

export const demoApi = {
  health: async (): Promise<HealthResponse> => {
    const snap = await loadSnapshot();
    return snap.health;
  },

  dimensions: async (platform: Platform, _env = 'prod'): Promise<DimensionsResponse> => {
    const snap = await loadSnapshot();
    return snap.platforms[platform].dimensions;
  },

  releases: async (platform: Platform, _env = 'prod'): Promise<{ releases: ReleaseRecord[] }> => {
    const snap = await loadSnapshot();
    return snap.platforms[platform].releases;
  },

  symbols: async (platform: Platform): Promise<{ symbols: SymbolRecord[] }> => {
    const snap = await loadSnapshot();
    return snap.platforms[platform].symbols;
  },

  overview: async (f: GlobalFilters): Promise<OverviewReport> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).overview;
  },

  timeseries: async (f: GlobalFilters): Promise<{ points: DailyTrendPoint[] }> => {
    const snap = await loadSnapshot();
    const pts = pickSlice(snap.platforms[f.platform], f).timeseries.points ?? [];
    return { points: filterPointsByDate(pts, f) };
  },

  breakdown: async (
    f: GlobalFilters,
    dimension: 'version' | 'adapter' | 'scene' | 'device' | 'os',
  ): Promise<{ dimension: string; slices: DimensionSlice[] }> => {
    const snap = await loadSnapshot();
    const sl = pickSlice(snap.platforms[f.platform], f);
    return sl.breakdowns[dimension] ?? { dimension, slices: [] };
  },

  sourceHealth: async (f: GlobalFilters): Promise<SourceHealthReport> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).sourceHealth;
  },

  compare: async (f: GlobalFilters, baseline: string, candidate: string): Promise<CompareReport> => {
    const snap = await loadSnapshot();
    return pickCompare(snap.platforms[f.platform], baseline, candidate).compare;
  },

  scanRegressions: async (f: GlobalFilters, baseline: string, candidate: string): Promise<RegressionScanReport> => {
    const snap = await loadSnapshot();
    return pickCompare(snap.platforms[f.platform], baseline, candidate).scanRegressions;
  },

  releaseGate: async (
    f: GlobalFilters,
    baseline: string,
    candidate: string,
    requireSymbolBound = false,
  ): Promise<ReleaseGateReport> => {
    const snap = await loadSnapshot();
    const bundle = pickCompare(snap.platforms[f.platform], baseline, candidate);
    return requireSymbolBound ? bundle.releaseGateSym : bundle.releaseGate;
  },

  startup: async (f: GlobalFilters): Promise<StartupReport> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).startup;
  },

  fluency: async (f: GlobalFilters): Promise<{ scenes: FluencySceneReport[] }> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).fluency;
  },

  resources: async (f: GlobalFilters): Promise<{ scenes: ResourceSceneReport[] }> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).resources;
  },

  network: async (f: GlobalFilters): Promise<NetworkReport> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).network;
  },

  networkSamples: async (
    f: GlobalFilters,
    operation?: string,
    onlyIssues = false,
    limit = 50,
  ): Promise<{ samples: NetworkTransportSample[] }> => {
    const snap = await loadSnapshot();
    const sl = pickSlice(snap.platforms[f.platform], f);
    let samples = (onlyIssues ? sl.networkSamplesIssues.samples : sl.networkSamples.samples) ?? [];
    if (operation) {
      samples = samples.filter((s) => s.operation === operation);
    }
    return { samples: samples.slice(0, limit) };
  },

  customJourneys: async (f: GlobalFilters): Promise<{ journeys: CustomJourneyReport[] }> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).customJourneys;
  },

  issues: async (f: GlobalFilters): Promise<{ issues: IssueSummary[] }> => {
    const snap = await loadSnapshot();
    const sl = pickSlice(snap.platforms[f.platform], f);
    let list = (sl.issues.issues ?? []).map(applyIssueOverlay);
    if (f.deviceModel) {
      list = list.filter((i) => !i.device_breakdown || f.deviceModel in i.device_breakdown);
    }
    if (f.osVersion) {
      list = list.filter((i) => !i.os_breakdown || f.osVersion in i.os_breakdown);
    }
    return { issues: list };
  },

  issueDetail: async (fingerprint: string, f: GlobalFilters): Promise<IssueSummary> => {
    const snap = await loadSnapshot();
    const p = snap.platforms[f.platform];
    const found =
      p.issueDetails[fingerprint] ||
      Object.values(snap.platforms).map((x) => x.issueDetails[fingerprint]).find(Boolean);
    if (!found) {
      throw new Error(`Issue ${fingerprint} not found in demo snapshot`);
    }
    return applyIssueOverlay(found);
  },

  updateIssueState: async (
    fingerprint: string,
    payload: {
      status: IssueStatus;
      assignee?: string;
      note?: string;
      resolved_in_version?: string;
      app_id?: string;
      platform?: Platform;
    },
    _apiKey?: string,
  ): Promise<IssueState> => {
    const next: IssueState = {
      fingerprint,
      status: payload.status,
      assignee: payload.assignee,
      note: payload.note,
      resolved_in_version: payload.resolved_in_version,
      updated_at_ms: Date.now(),
    };
    issueStateOverlay.set(fingerprint, next);
    recordAudit('update_issue_state', 'issue_fingerprint', fingerprint, payload.note || payload.status, 1);
    return next;
  },

  policy: async (appId: string, platform: Platform, env = 'prod'): Promise<SamplingPolicy> => {
    const key = `${appId}|${platform}|${env}`;
    const ov = policyOverlay.get(key);
    if (ov) return ov;
    const snap = await loadSnapshot();
    const existing = snap.platforms[platform].policy;
    if (existing) return existing;
    return {
      policy_id: `pol-${platform}-${env}`,
      app_id: appId,
      platform,
      env,
      rate_ppm: 1_000_000,
      lease_ttl_ms: 3_600_000,
      max_events_24h: 500,
      enabled_collectors: ['startup', 'fluency', 'resources', 'network', 'stability', 'cpu_profile', 'heap_snapshot'],
      updated_at_ms: Date.now(),
    };
  },

  upsertPolicy: async (policy: SamplingPolicy, _apiKey?: string): Promise<SamplingPolicy> => {
    const saved: SamplingPolicy = { ...policy, updated_at_ms: Date.now() };
    policyOverlay.set(`${saved.app_id}|${saved.platform}|${saved.env}`, saved);
    recordAudit('upsert_policy', 'policy', saved.policy_id, `rate_ppm=${saved.rate_ppm}`, 1);
    return saved;
  },

  lookupSession: async (
    params: { installHash?: string; userHash?: string; reason: string; platform?: Platform },
    _apiKey?: string,
  ): Promise<SessionLookupResponse> => {
    const snap = await loadSnapshot();
    const p = snap.platforms[params.platform || 'android'];
    const isInstall = Boolean(params.installHash && !params.userHash);
    const hash = (isInstall ? params.installHash : params.userHash) || '0000111122223333';
    const map = isInstall ? p.sessionsByInstall : p.sessionsByUser;
    const exact = map[hash] || Object.values(map)[0] || Object.values(p.sessionsByUser)[0];
    const events = exact?.events ?? [];
    recordAudit('session_lookup', isInstall ? 'install_hash' : 'user_hash', hash, params.reason, events.length);
    return {
      target_type: isInstall ? 'install_hash' : 'user_hash',
      target_hash: hash,
      reason: params.reason,
      actor: 'demo-visitor',
      events,
    };
  },

  auditLogs: async (_apiKey?: string): Promise<{ audit_logs: AuditLogEntry[] }> => {
    const snap = await loadSnapshot();
    return { audit_logs: [...auditOverlay, ...(snap.auditLogs.audit_logs ?? [])].slice(0, 50) };
  },

  cpuProfiles: async (f: GlobalFilters, runtime?: string): Promise<{ profiles: CPUProfileRecord[] }> => {
    const snap = await loadSnapshot();
    let profiles = pickSlice(snap.platforms[f.platform], f).cpuProfiles.profiles ?? [];
    if (runtime) {
      profiles = profiles.filter((p) => p.runtime === runtime);
    }
    return { profiles };
  },

  flamegraph: async (profileId: string): Promise<FlamegraphReport> => {
    const snap = await loadSnapshot();
    for (const p of Object.values(snap.platforms)) {
      if (p.flamegraphs[profileId]) return p.flamegraphs[profileId];
    }
    throw new Error(`Profile ${profileId} not found in demo snapshot`);
  },

  heapSnapshots: async (f: GlobalFilters): Promise<{ snapshots: HeapSnapshotRecord[] }> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).heapSnapshots;
  },

  compareHeap: async (f: GlobalFilters, baseline: string, candidate: string): Promise<HeapCompareReport> => {
    const snap = await loadSnapshot();
    const cmp = pickCompare(snap.platforms[f.platform], baseline, candidate).compareHeap;
    if (!cmp) {
      throw new Error(`No heap comparison available for ${baseline} -> ${candidate}`);
    }
    return cmp;
  },

  remoteLogTasks: async (platform: Platform): Promise<{ tasks: RemoteLogTask[] }> => {
    const snap = await loadSnapshot();
    const base = snap.platforms[platform].remoteLogTasks.tasks ?? [];
    const extra = remoteTasksOverlay.get(platform) ?? [];
    return { tasks: [...extra, ...base] };
  },

  createRemoteLogTask: async (
    payload: {
      app_id: string;
      platform: Platform;
      target_type: 'user_hash' | 'install_hash';
      target_hash: string;
      reason: string;
      max_bytes?: number;
    },
    _apiKey?: string,
  ): Promise<RemoteLogTask> => {
    const now = Date.now();
    const task: RemoteLogTask = {
      task_id: `demo-task-${now.toString(36)}`,
      app_id: payload.app_id,
      platform: payload.platform,
      target_type: payload.target_type,
      target_hash: payload.target_hash,
      reason: payload.reason,
      requested_by: 'demo-visitor',
      status: 'completed',
      max_bytes: payload.max_bytes ?? 65536,
      created_at_ms: now,
      expires_at_ms: now + 3_600_000,
      completed_at_ms: now + 850,
      uploaded_bytes: 512,
      sha256: 'demo000000000000000000000000000000000000000000000000000000000000',
    };
    const prev = remoteTasksOverlay.get(payload.platform) ?? [];
    remoteTasksOverlay.set(payload.platform, [task, ...prev]);
    recordAudit('create_remote_log_task', payload.target_type, payload.target_hash, payload.reason, 1);
    return task;
  },

  remoteLogContent: async (
    taskId: string,
    reason: string,
    _apiKey?: string,
  ): Promise<{ task: RemoteLogTask; content: string }> => {
    const snap = await loadSnapshot();
    for (const p of Object.values(snap.platforms)) {
      if (p.remoteLogContents[taskId]) {
        recordAudit('read_remote_log', 'task_id', taskId, reason, 1);
        return p.remoteLogContents[taskId];
      }
    }
    for (const list of remoteTasksOverlay.values()) {
      const hit = list.find((t) => t.task_id === taskId);
      if (hit) {
        recordAudit('read_remote_log', 'task_id', taskId, reason, 1);
        return {
          task: hit,
          content: [
            `${new Date().toISOString()} [INFO] CaliperRingLog (Demo Mode): target=${hit.target_hash}`,
            `${new Date().toISOString()} [INFO] Scene transition: home_feed -> streaming_reader (adapter=native)`,
            `${new Date().toISOString()} [INFO] PII scrubber active: email=[REDACTED_EMAIL] auth=Bearer [REDACTED_TOKEN]`,
          ].join('\n'),
        };
      }
    }
    throw new Error(`Remote log task ${taskId} not found in demo snapshot`);
  },

  otlpExport: async (f: GlobalFilters): Promise<Record<string, unknown>> => {
    const snap = await loadSnapshot();
    return pickSlice(snap.platforms[f.platform], f).otlpExport;
  },

  alertRules: async (platform: Platform): Promise<{ rules: AlertRule[] }> => {
    const snap = await loadSnapshot();
    if (!alertRulesOverlay.has(platform)) {
      alertRulesOverlay.set(platform, [...(snap.platforms[platform].alertRules.rules ?? [])]);
    }
    return { rules: alertRulesOverlay.get(platform) ?? [] };
  },

  alertIncidents: async (): Promise<{ incidents: AlertIncident[] }> => {
    const snap = await loadSnapshot();
    return snap.alertIncidents;
  },

  upsertAlertRule: async (rule: AlertRule, _apiKey?: string): Promise<AlertRule> => {
    const snap = await loadSnapshot();
    const list = alertRulesOverlay.get(rule.platform) ?? [...(snap.platforms[rule.platform].alertRules.rules ?? [])];
    const saved: AlertRule = { ...rule, rule_id: rule.rule_id || `demo-rule-${Date.now().toString(36)}` };
    const idx = list.findIndex((r) => r.rule_id === saved.rule_id);
    if (idx >= 0) list[idx] = saved;
    else list.unshift(saved);
    alertRulesOverlay.set(rule.platform, list);
    return saved;
  },

  deleteAlertRule: async (ruleId: string, _apiKey?: string): Promise<{ status: string; rule_id: string }> => {
    for (const [plat, list] of alertRulesOverlay.entries()) {
      alertRulesOverlay.set(plat, list.filter((r) => r.rule_id !== ruleId));
    }
    return { status: 'deleted', rule_id: ruleId };
  },

  testAlertWebhook: async (
    ruleId: string,
    _apiKey?: string,
  ): Promise<{
    rule_id: string;
    format: string;
    receipt: { delivery_status: string; delivery_http_status?: number; delivery_error?: string };
  }> => ({
    rule_id: ruleId,
    format: 'demo_simulated',
    receipt: {
      delivery_status: 'simulated_ok_in_static_demo',
      delivery_http_status: 200,
    },
  }),

  evaluateAlerts: async (
    _apiKey?: string,
  ): Promise<{ outcomes: Array<{ rule_id: string; state: string; gate_reason?: string; sample_size: number }> }> => ({
    outcomes: [
      { rule_id: 'demo-startup-p90-android', state: 'ok', sample_size: 420 },
      { rule_id: 'demo-startup-p90-ios', state: 'ok', sample_size: 380 },
    ],
  }),
};
