export type Platform = 'android' | 'ios';

export interface HealthResponse {
  status: string;
  uptime_seconds: number;
  storage: {
    engine: string;
    total_events: number;
    release_count: number;
    issue_count: number;
  };
}

export interface ReleaseRecord {
  app_id: string;
  platform: Platform;
  env: string;
  app_version: string;
  build_number: string;
  git_sha?: string;
  symbol_id?: string;
  adapter?: string;
  registered: boolean;
  first_seen_ms: number;
  last_seen_ms: number;
  event_count: number;
}

export interface SymbolRecord {
  symbol_id: string;
  app_id: string;
  platform: Platform;
  env: string;
  app_version: string;
  build_number: string;
  git_sha?: string;
  kind: string;
  sha256: string;
  size_bytes: number;
  uploaded_at_ms: number;
}

export interface QuantileResult {
  value: number | null;
  interval?: [number, number];
  sample_size: number;
  missing_reason?: string;
}

export interface StartupReport {
  platform: Platform;
  app_version?: string;
  success_count: number;
  aborted_count: number;
  timeout_count: number;
  staged_coverage_pct: number | null;
  total_p50_ms: QuantileResult;
  total_p90_ms: QuantileResult;
  native_init_p90_ms: QuantileResult;
  runtime_init_p90_ms: QuantileResult;
  first_frame_p90_ms: QuantileResult;
}

export interface FluencySceneReport {
  scene: string;
  algorithm: string;
  total_windows: number;
  valid_windows: number;
  zero_frame_windows: number;
  total_frames: number;
  jank_frames: number;
  jank_rate_pct: number | null;
  severity: string;
  missing_reason?: string;
  frame_p90_ms: QuantileResult;
  stall_window_count: number;
  sample_stall_stacks?: string[][];
}

export interface ResourceSceneReport {
  scene: string;
  memory_kind?: string;
  resource_windows: number;
  avg_cpu_pct: number | null;
  cpu_missing_reason?: string;
  memory_peak_p50_mib: QuantileResult;
  memory_peak_p90_mib: QuantileResult;
  js_longtask_windows: number;
  js_longtask_count: number;
  avg_js_blocking_ms_per_window: number | null;
  js_max_task_ms: number | null;
}

export interface TransportOpReport {
  operation: string;
  method: string;
  sample_count: number;
  success_rate_pct: number | null;
  connection_reuse_rate_pct: number | null;
  dns_p90_ms: QuantileResult;
  tcp_p90_ms: QuantileResult;
  tls_p90_ms: QuantileResult;
  ttfb_p90_ms: QuantileResult;
  body_p90_ms: QuantileResult;
  total_p90_ms: QuantileResult;
}

export interface NetworkReport {
  summary_windows: number;
  total_calls: number;
  failed_calls: number;
  success_rate_pct: number | null;
  missing_reason?: string;
  duration_p50_ms: QuantileResult;
  duration_p90_ms: QuantileResult;
  operations: TransportOpReport[];
}

export interface StackFrame {
  module?: string;
  symbol: string;
  file?: string;
  line?: number;
  address?: string;
  in_app?: boolean;
}

export interface Breadcrumb {
  timestamp_ms: number;
  category: string;
  message: string;
}

export interface IssueSummary {
  fingerprint: string;
  kind: string;
  title: string;
  fatal: boolean;
  exception_type?: string;
  exit_reason?: string;
  event_count: number;
  affected_installs: number;
  first_seen_ms: number;
  last_seen_ms: number;
  releases: string[];
  sample_frames?: StackFrame[];
  sample_breadcrumbs?: Breadcrumb[];
  symbol_status?: string;
  resolved_frames?: number;
  matched_symbols?: string[];
}

export interface OverviewReport {
  platform: Platform;
  app_version?: string;
  startup: StartupReport;
  fluency: FluencySceneReport[];
  resources: ResourceSceneReport[];
  network: NetworkReport;
  top_issues: IssueSummary[];
}

export interface MetricComparison {
  metric_id: string;
  unit: string;
  direction: 'lower_is_better' | 'higher_is_better';
  baseline_value: number | null;
  baseline_sample_size: number;
  candidate_value: number | null;
  candidate_sample_size: number;
  absolute_delta: number | null;
  relative_pct: number | null;
  status: 'regressed' | 'improved' | 'neutral' | 'gated';
  gate_reason?: string;
  stat_test?: 'two_proportion_z' | 'histogram_ks' | 'threshold_only';
  p_value?: number;
  statistically_significant?: boolean;
  baseline_ci95?: [number, number];
  candidate_ci95?: [number, number];
}

export interface RegressionFinding {
  scope: string;
  scene?: string;
  algorithm?: string;
  impact_score: number;
  comparison: MetricComparison;
}

export interface RegressionScanReport {
  platform: Platform;
  baseline_version: string;
  candidate_version: string;
  alpha: number;
  verdict: string;
  regressed_count: number;
  findings: RegressionFinding[];
}

export interface SessionTimelineEvent {
  event_id: string;
  occurred_at_ms: number;
  app_id: string;
  platform: Platform;
  env: string;
  app_version: string;
  build_number: string;
  adapter?: string;
  install_hash: string;
  user_hash?: string;
  kind: string;
  scene?: string;
  summary: string;
}

export interface SessionLookupResponse {
  target_type: 'install_hash' | 'user_hash';
  target_hash: string;
  reason: string;
  actor: string;
  events: SessionTimelineEvent[];
}

export interface AuditLogEntry {
  audit_id: string;
  timestamp_ms: number;
  actor: string;
  role: string;
  action: string;
  target_type: string;
  target_hash: string;
  reason: string;
  remote_ip?: string;
  result_count: number;
}

export interface CPUProfileRecord {
  profile_id: string;
  occurred_at_ms: number;
  app_id: string;
  platform: Platform;
  env: string;
  app_version: string;
  build_number: string;
  scene: string;
  runtime: 'native' | 'hermes';
  thread_name?: string;
  sample_interval_ms: number;
  duration_ms: number;
  total_samples: number;
}

export interface FlameNode {
  name: string;
  depth: number;
  self_samples: number;
  total_samples: number;
  self_pct: number;
  total_pct: number;
  children?: FlameNode[];
}

export interface FlamegraphReport {
  profile_id: string;
  platform: Platform;
  app_version: string;
  build_number: string;
  scene: string;
  runtime: 'native' | 'hermes';
  thread_name?: string;
  sample_interval_ms: number;
  duration_ms: number;
  total_samples: number;
  symbolicated: boolean;
  root: FlameNode;
}

export interface HeapDominator {
  class_name: string;
  instance_count: number;
  retained_kib: number;
  retainer_path?: string[];
}

export interface HeapSnapshotRecord {
  snapshot_id: string;
  occurred_at_ms: number;
  platform: Platform;
  app_version: string;
  build_number: string;
  scene: string;
  trigger_reason: string;
  memory_kind: string;
  total_allocated_kib: number;
  total_retained_kib: number;
  memory_limit_kib?: number;
  dominators: HeapDominator[];
}

export interface HeapDominatorDiff {
  class_name: string;
  baseline_mib: number;
  candidate_mib: number;
  delta_mib: number;
  baseline_instances: number;
  candidate_instances: number;
  suspect: boolean;
}

export interface HeapCompareReport {
  platform: Platform;
  baseline_version: string;
  candidate_version: string;
  baseline_retained_mib: number;
  candidate_retained_mib: number;
  delta_retained_mib: number;
  dominator_diffs: HeapDominatorDiff[];
}

export interface RemoteLogTask {
  task_id: string;
  app_id: string;
  platform: Platform;
  target_type: 'install_hash' | 'user_hash';
  target_hash: string;
  reason: string;
  requested_by: string;
  status: 'pending' | 'completed' | 'expired';
  max_bytes: number;
  created_at_ms: number;
  expires_at_ms: number;
  completed_at_ms?: number;
  uploaded_bytes?: number;
  sha256?: string;
}

export interface CompareReport {
  platform: Platform;
  baseline_version: string;
  candidate_version: string;
  verdict: 'regressed' | 'improved' | 'neutral' | 'inconclusive';
  regressed_count: number;
  gated_count: number;
  metrics: MetricComparison[];
}

export interface AlertRule {
  rule_id: string;
  name: string;
  app_id: string;
  platform: Platform;
  env: string;
  app_version?: string;
  scene?: string;
  metric_id: string;
  operator: 'gt' | 'lt';
  threshold: number;
  min_samples: number;
  webhook_url: string;
  webhook_format: 'generic' | 'feishu' | 'slack' | 'discord';
  cooldown_sec: number;
  enabled: boolean;
  updated_at_ms?: number;
}

export interface AlertIncident {
  incident_id: string;
  rule_id: string;
  rule_name: string;
  app_id: string;
  platform: Platform;
  metric_id: string;
  status: 'firing' | 'resolved';
  observed_value: number | null;
  threshold: number;
  sample_size: number;
  triggered_at_ms: number;
  last_evaluated_ms: number;
  resolved_at_ms?: number;
  delivery_status?: string;
  delivery_http_status?: number;
  delivery_error?: string;
}
