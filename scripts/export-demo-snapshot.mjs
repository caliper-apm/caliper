#!/usr/bin/env node
/**
 * Exports a real, pre-computed Console 2.4 demo snapshot from a live Caliper APM server
 * (seeded via `seed-replay` + R8/SourceMap/dSYM symbol uploads) into a static JSON bundle
 * for zero-backend interactive GitHub Pages hosting (`VITE_DEMO_MODE=true`).
 *
 * Usage:
 *   node scripts/export-demo-snapshot.mjs --server http://127.0.0.1:18080 --out console/public/demo-snapshot.json
 */

import fs from 'node:fs';
import path from 'node:path';

function parseArgs(argv) {
  const out = {
    server: 'http://127.0.0.1:18080',
    outFile: 'console/public/demo-snapshot.json',
  };
  for (let i = 2; i < argv.length; i += 1) {
    const a = argv[i];
    if (a === '--server' && argv[i + 1]) out.server = argv[++i].replace(/\/+$/, '');
    else if (a === '--out' && argv[i + 1]) out.outFile = argv[++i];
  }
  return out;
}

const { server, outFile } = parseArgs(process.argv);

async function req(urlPath, init) {
  const res = await fetch(`${server}${urlPath}`, init);
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`${init?.method || 'GET'} ${urlPath} failed (${res.status}): ${text.slice(0, 240)}`);
  }
  return JSON.parse(text);
}

async function reqSafe(urlPath, init, fallback = null) {
  try {
    return await req(urlPath, init);
  } catch (err) {
    console.warn(`[demo-snapshot] optional ${urlPath} skipped: ${err.message}`);
    return fallback;
  }
}

function qs(params) {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') sp.set(k, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

function sliceKey(appVersion = '', adapter = '', scene = '') {
  return `v=${appVersion}|a=${adapter}|s=${scene}`;
}

async function seedDemoExtras() {
  // Ensure at least one completed remote diagnostic log task & one alert rule exist so the
  // Diagnostics & Operations pages showcase real interactive records in the static demo.
  const existingTasks = await reqSafe('/v1/diagnostics/log-tasks?platform=android', undefined, { tasks: [] });
  if (!existingTasks.tasks || existingTasks.tasks.length === 0) {
    const task = await reqSafe('/v1/diagnostics/log-tasks', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        app_id: 'dev.caliper.demo',
        platform: 'android',
        target_type: 'user_hash',
        target_hash: '0000111122223333',
        reason: 'DEMO-INC-1042',
        max_bytes: 65536,
      }),
    });
    if (task?.task_id) {
      await reqSafe(`/v1/diagnostics/log-tasks/${encodeURIComponent(task.task_id)}/upload`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          content: [
            '2026-11-24T08:12:01.104Z [INFO] CaliperBoot: cold_start native=142ms runtime=188ms first_frame=240ms',
            '2026-11-24T08:12:04.512Z [INFO] Scene: home_feed -> streaming_reader (adapter=react-native)',
            '2026-11-24T08:12:07.881Z [WARN] LooperMonitor: main thread dispatch 184ms in FeedImageDecoder.decode()',
            '2026-11-24T08:12:09.102Z [ERROR] OkHttp: POST /v1/feed/rank status=502 ttfb=412ms traceparent=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01',
            '2026-11-24T08:12:09.218Z [INFO] RingLog: redacted user=[REDACTED_EMAIL] token=Bearer [REDACTED_TOKEN]',
          ].join('\n'),
        }),
      });
    }
  }

  for (const platform of ['android', 'ios']) {
    const existingRules = await reqSafe(`/v1/alerts/rules?platform=${platform}`, undefined, { rules: [] });
    if (!existingRules.rules || existingRules.rules.length === 0) {
      await reqSafe('/v1/alerts/rules', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          rule_id: `demo-startup-p90-${platform}`,
          name: platform === 'android' ? 'Android Cold Start P90 Regression Guard' : 'iOS Cold Start P90 Regression Guard',
          app_id: 'dev.caliper.demo',
          platform,
          env: 'prod',
          metric_id: 'startup_cold_p90_ms',
          operator: 'gt',
          threshold: 950,
          min_sample_size: 20,
          cooldown_minutes: 30,
          webhook_format: 'feishu',
          enabled: true,
        }),
      });
    }
  }
  await reqSafe('/v1/alerts/evaluate', { method: 'POST' });
}

async function exportSlice(platform, env, appVersion, adapter, scene) {
  const base = {
    platform,
    env,
    app_version: appVersion || undefined,
    adapter: adapter || undefined,
    scene: scene || undefined,
  };
  const q = qs(base);

  const [
    overview,
    timeseries,
    startup,
    fluency,
    resources,
    network,
    networkSamples,
    networkSamplesIssues,
    customJourneys,
    issues,
    sourceHealth,
    cpuProfiles,
    heapSnapshots,
    otlpExport,
  ] = await Promise.all([
    req(`/v1/analytics/overview${q}`),
    req(`/v1/analytics/timeseries${q}`),
    req(`/v1/analytics/startup${q}`),
    req(`/v1/analytics/fluency${q}`),
    req(`/v1/analytics/resources${q}`),
    req(`/v1/analytics/network${q}`),
    req(`/v1/analytics/network/samples${qs({ ...base, limit: 50 })}`),
    req(`/v1/analytics/network/samples${qs({ ...base, only_issues: 'true', limit: 50 })}`),
    req(`/v1/analytics/journeys${q}`),
    req(`/v1/stability/issues${q}`),
    req(`/v1/ops/source-health${q}`),
    req(`/v1/diagnostics/profiles${q}`),
    req(`/v1/diagnostics/heap${q}`),
    req(`/v1/otlp/export${q}`),
  ]);

  const breakdowns = {};
  for (const dim of ['version', 'adapter', 'scene', 'device', 'os']) {
    const dimParams = {
      platform,
      env,
      app_version: dim === 'version' ? undefined : (appVersion || undefined),
      adapter: dim === 'adapter' ? undefined : (adapter || undefined),
      scene: dim === 'scene' ? undefined : (scene || undefined),
      dimension: dim,
    };
    breakdowns[dim] = await req(`/v1/analytics/breakdown${qs(dimParams)}`);
  }

  return {
    overview,
    timeseries,
    startup,
    fluency,
    resources,
    network,
    networkSamples,
    networkSamplesIssues,
    customJourneys,
    issues,
    sourceHealth,
    cpuProfiles,
    heapSnapshots,
    otlpExport,
    breakdowns,
  };
}

async function main() {
  await seedDemoExtras();

  const health = await req('/healthz');
  const alertIncidents = await reqSafe('/v1/alerts/incidents', undefined, { incidents: [] });

  const platforms = {};
  for (const platform of ['android', 'ios']) {
    const env = 'prod';
    const [dimensions, releasesRes, symbolsRes, alertRulesRes, remoteLogTasksRes] = await Promise.all([
      req(`/v1/ops/dimensions${qs({ platform, env })}`),
      req(`/v1/releases${qs({ platform, env })}`),
      req(`/v1/symbols${qs({ platform })}`),
      req(`/v1/alerts/rules${qs({ platform })}`),
      req(`/v1/diagnostics/log-tasks${qs({ platform })}`),
    ]);

    const appId = releasesRes.releases?.[0]?.app_id || 'dev.caliper.demo';
    const policy = await reqSafe(`/v1/policy${qs({ app_id: appId, platform, env })}`, undefined, null);

    const remoteLogContents = {};
    for (const task of remoteLogTasksRes.tasks || []) {
      if (task.status === 'completed') {
        const c = await reqSafe(
          `/v1/diagnostics/log-tasks/${encodeURIComponent(task.task_id)}/content${qs({ reason: 'DEMO-EXPORT' })}`,
        );
        if (c) remoteLogContents[task.task_id] = c;
      }
    }

    const versions = Array.from(new Set((releasesRes.releases || []).map((r) => r.app_version))).sort();
    const scenes = (dimensions.scenes || []).slice(0, 6);

    const slices = {};
    // 1. Platform default
    slices[sliceKey('', '', '')] = await exportSlice(platform, env, '', '', '');
    // 2. Per release version
    for (const v of versions) {
      slices[sliceKey(v, '', '')] = await exportSlice(platform, env, v, '', '');
    }
    // 3. Per adapter (native vs react-native)
    for (const a of ['native', 'react-native']) {
      slices[sliceKey('', a, '')] = await exportSlice(platform, env, '', a, '');
    }
    // 4. Per scene
    for (const s of scenes) {
      slices[sliceKey('', '', s)] = await exportSlice(platform, env, '', '', s);
    }

    // Release comparisons (all ordered pairs, e.g. 1.0.0->1.1.0, 1.1.0->1.2.0, 1.0.0->1.2.0)
    const compares = {};
    for (let i = 0; i < versions.length; i += 1) {
      for (let j = i + 1; j < versions.length; j += 1) {
        const baseline = versions[i];
        const candidate = versions[j];
        const pairKey = `${baseline}->${candidate}`;
        const jsonHeaders = { 'Content-Type': 'application/json' };
        const [compare, scanRegressions, releaseGate, releaseGateSym, compareHeap] = await Promise.all([
          req('/v1/analytics/compare', {
            method: 'POST',
            headers: jsonHeaders,
            body: JSON.stringify({ platform, env, baseline_version: baseline, candidate_version: candidate }),
          }),
          req('/v1/analytics/regressions/scan', {
            method: 'POST',
            headers: jsonHeaders,
            body: JSON.stringify({ platform, env, baseline_version: baseline, candidate_version: candidate }),
          }),
          req('/v1/analytics/release-gate', {
            method: 'POST',
            headers: jsonHeaders,
            body: JSON.stringify({
              platform,
              env,
              baseline_version: baseline,
              candidate_version: candidate,
              require_symbol_bound: false,
            }),
          }),
          req('/v1/analytics/release-gate', {
            method: 'POST',
            headers: jsonHeaders,
            body: JSON.stringify({
              platform,
              env,
              baseline_version: baseline,
              candidate_version: candidate,
              require_symbol_bound: true,
            }),
          }),
          reqSafe('/v1/diagnostics/heap/compare', {
            method: 'POST',
            headers: jsonHeaders,
            body: JSON.stringify({ platform, env, baseline_version: baseline, candidate_version: candidate }),
          }),
        ]);
        compares[pairKey] = { compare, scanRegressions, releaseGate, releaseGateSym, compareHeap };
      }
    }

    // Issue details for every unique fingerprint across all slices
    const fps = new Set();
    for (const sl of Object.values(slices)) {
      for (const iss of sl.issues?.issues || []) {
        if (iss.fingerprint) fps.add(iss.fingerprint);
      }
    }
    const issueDetails = {};
    const sampleUserHashes = new Set(['0000111122223333']);
    const sampleInstallHashes = new Set();
    for (const fp of fps) {
      const detail = await reqSafe(`/v1/stability/issues/${encodeURIComponent(fp)}${qs({ platform, env })}`);
      if (detail) {
        issueDetails[fp] = detail;
        if (detail.sample_user_hash) sampleUserHashes.add(detail.sample_user_hash);
        if (detail.sample_install_hash) sampleInstallHashes.add(detail.sample_install_hash);
      }
    }

    // Flamegraphs for every unique CPU profile
    const profileIds = new Set();
    for (const sl of Object.values(slices)) {
      for (const p of sl.cpuProfiles?.profiles || []) {
        if (p.profile_id) profileIds.add(p.profile_id);
      }
    }
    const flamegraphs = {};
    for (const pid of profileIds) {
      const fg = await reqSafe(`/v1/diagnostics/profiles/${encodeURIComponent(pid)}/flamegraph`);
      if (fg) flamegraphs[pid] = fg;
    }

    // Session timelines for default + discovered user/install hashes
    const sessionsByUser = {};
    for (const uh of sampleUserHashes) {
      const res = await reqSafe(
        `/v1/sessions/lookup${qs({ user_hash: uh, reason: 'DEMO-EXPORT', platform })}`,
      );
      if (res && res.events?.length) sessionsByUser[uh] = res;
    }
    const sessionsByInstall = {};
    for (const ih of sampleInstallHashes) {
      const res = await reqSafe(
        `/v1/sessions/lookup${qs({ install_hash: ih, reason: 'DEMO-EXPORT', platform })}`,
      );
      if (res && res.events?.length) sessionsByInstall[ih] = res;
    }

    platforms[platform] = {
      appId,
      dimensions,
      releases: releasesRes,
      symbols: symbolsRes,
      policy,
      alertRules: alertRulesRes,
      remoteLogTasks: remoteLogTasksRes,
      remoteLogContents,
      slices,
      compares,
      issueDetails,
      flamegraphs,
      sessionsByUser,
      sessionsByInstall,
    };
  }

  const auditLogs = await reqSafe('/v1/audit/logs?limit=50', undefined, { audit_logs: [] });

  const snapshot = {
    meta: {
      generated_at_utc: new Date().toISOString(),
      total_events: health?.storage?.total_events ?? 0,
      scenario: 'v1.0.0 -> v1.1.0 (regressed) -> v1.2.0 (fixed)',
    },
    health,
    alertIncidents,
    auditLogs,
    platforms,
  };

  fs.mkdirSync(path.dirname(outFile), { recursive: true });
  const payload = JSON.stringify(snapshot);
  fs.writeFileSync(outFile, payload, 'utf8');
  console.log(
    `[demo-snapshot] wrote ${outFile} (${(Buffer.byteLength(payload) / 1024).toFixed(1)} KiB, events=${snapshot.meta.total_events})`,
  );
}

main().catch((err) => {
  console.error('[demo-snapshot] fatal:', err);
  process.exit(1);
});
