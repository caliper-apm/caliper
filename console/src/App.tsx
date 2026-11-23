import { useEffect, useMemo, useState } from 'react';
import { api } from './api.ts';
import { t, type Lang } from './i18n.ts';
import { OverviewPage } from './pages/OverviewPage.tsx';
import { StabilityPage } from './pages/StabilityPage.tsx';
import { FluencyPage } from './pages/FluencyPage.tsx';
import { ResourcesNetworkPage } from './pages/ResourcesNetworkPage.tsx';
import { DiagnosticsPage } from './pages/DiagnosticsPage.tsx';
import { OperationsPage } from './pages/OperationsPage.tsx';
import type {
  AdapterFilter,
  DimensionsResponse,
  EnvName,
  GlobalFilters,
  HealthResponse,
  Platform,
  ReleaseRecord,
} from './types.ts';

type Tab = 'overview' | 'stability' | 'fluency' | 'resources' | 'diagnostics' | 'operations';

const VALID_TABS: Tab[] = ['overview', 'stability', 'fluency', 'resources', 'diagnostics', 'operations'];
const DEFAULT_SCENES = ['home_feed', 'media_gallery', 'streaming_reader'];

function readInitialQuery() {
  const sp = new URLSearchParams(window.location.search);
  const rawTab = sp.get('tab') as Tab | null;
  const rawPlatform = sp.get('platform');
  const rawEnv = sp.get('env');
  const rawAdapter = sp.get('adapter');
  const rawRange = Number(sp.get('range') ?? '14');
  return {
    tab: rawTab && VALID_TABS.includes(rawTab) ? rawTab : ('overview' as Tab),
    platform: (rawPlatform === 'ios' ? 'ios' : 'android') as Platform,
    env: (rawEnv === 'staging' || rawEnv === 'dev' ? rawEnv : 'prod') as EnvName,
    appVersion: sp.get('version') ?? '',
    adapter: (rawAdapter === 'native' || rawAdapter === 'react-native' ? rawAdapter : '') as AdapterFilter,
    scene: sp.get('scene') ?? '',
    deviceModel: sp.get('device') ?? '',
    osVersion: sp.get('os') ?? '',
    rangeDays: (rawRange === 0 || rawRange === 7 || rawRange === 14 ? rawRange : 14) as 0 | 7 | 14,
  };
}

function computeDateWindow(rangeDays: 0 | 7 | 14, latestDay?: string): { startDayUtc?: string; endDayUtc?: string } {
  if (rangeDays === 0 || !latestDay) return {};
  const end = new Date(`${latestDay}T00:00:00Z`);
  if (Number.isNaN(end.getTime())) return {};
  const start = new Date(end.getTime() - (rangeDays - 1) * 86400_000);
  return {
    startDayUtc: start.toISOString().slice(0, 10),
    endDayUtc: latestDay,
  };
}

export function App() {
  const init = useMemo(() => readInitialQuery(), []);
  const [lang, setLang] = useState<Lang>('zh');
  const [theme, setTheme] = useState<'light' | 'dark'>(() => {
    const saved = window.localStorage.getItem('caliper-theme');
    return saved === 'dark' ? 'dark' : 'light';
  });
  const [tab, setTab] = useState<Tab>(init.tab);
  const [platform, setPlatform] = useState<Platform>(init.platform);
  const [env, setEnv] = useState<EnvName>(init.env);
  const [appVersion, setAppVersion] = useState(init.appVersion);
  const [adapter, setAdapter] = useState<AdapterFilter>(init.adapter);
  const [scene, setScene] = useState(init.scene);
  const [deviceModel, setDeviceModel] = useState(init.deviceModel);
  const [osVersion, setOsVersion] = useState(init.osVersion);
  const [rangeDays, setRangeDays] = useState<0 | 7 | 14>(init.rangeDays);
  const [latestDay, setLatestDay] = useState<string | undefined>(undefined);

  const [releases, setReleases] = useState<ReleaseRecord[]>([]);
  const [dims, setDims] = useState<DimensionsResponse>({ devices: [], os_versions: [], scenes: DEFAULT_SCENES });
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);
  const [focusIssueFP, setFocusIssueFP] = useState<string | null>(null);
  const [focusTraceTarget, setFocusTraceTarget] = useState<{ targetType: 'user_hash' | 'install_hash'; hash: string } | null>(null);
  const [copiedLink, setCopiedLink] = useState(false);
  const [exporting, setExporting] = useState(false);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    window.localStorage.setItem('caliper-theme', theme);
  }, [theme]);

  useEffect(() => {
    const sp = new URLSearchParams();
    sp.set('tab', tab);
    sp.set('platform', platform);
    if (env !== 'prod') sp.set('env', env);
    if (appVersion) sp.set('version', appVersion);
    if (adapter) sp.set('adapter', adapter);
    if (scene) sp.set('scene', scene);
    if (deviceModel) sp.set('device', deviceModel);
    if (osVersion) sp.set('os', osVersion);
    if (rangeDays !== 14) sp.set('range', String(rangeDays));
    const qs = sp.toString();
    window.history.replaceState(null, '', qs ? `?${qs}` : '/');
  }, [tab, platform, env, appVersion, adapter, scene, deviceModel, osVersion, rangeDays]);

  function openIssueDetail(fingerprint: string) {
    setFocusIssueFP(fingerprint);
    setTab('stability');
  }

  function traceUserSession(targetType: 'user_hash' | 'install_hash', hash: string) {
    setFocusTraceTarget({ targetType, hash });
    setTab('diagnostics');
  }

  function drillToScene(targetScene: string) {
    setScene(targetScene);
    setTab('fluency');
  }

  useEffect(() => {
    let active = true;
    api.health().then(h => { if (active) setHealth(h); }).catch(() => {});
    Promise.all([
      api.releases(platform, env),
      api.dimensions(platform, env).catch(() => ({ devices: [], os_versions: [], scenes: DEFAULT_SCENES })),
    ])
      .then(([res, dimRes]) => {
        if (!active) return;
        const list = res.releases ?? [];
        setReleases(list);
        setDims({
          devices: dimRes.devices ?? [],
          os_versions: dimRes.os_versions ?? [],
          scenes: (dimRes.scenes?.length ?? 0) > 0 ? dimRes.scenes : DEFAULT_SCENES,
        });
        const maxMS = list.reduce((m, r) => Math.max(m, r.last_seen_ms || 0), 0);
        if (maxMS > 0) {
          setLatestDay(new Date(maxMS).toISOString().slice(0, 10));
        }
      })
      .catch(() => { if (active) setReleases([]); });
    return () => { active = false; };
  }, [platform, env, refreshTick]);

  const versions = useMemo(() => Array.from(new Set(releases.map(r => r.app_version))).sort(), [releases]);

  const filters: GlobalFilters = useMemo(() => {
    const { startDayUtc, endDayUtc } = computeDateWindow(rangeDays, latestDay);
    return { platform, env, appVersion, adapter, scene, deviceModel, osVersion, rangeDays, startDayUtc, endDayUtc };
  }, [platform, env, appVersion, adapter, scene, deviceModel, osVersion, rangeDays, latestDay]);

  const navGroups: Array<{ label: string; items: Array<{ id: Tab; label: string; icon: string }> }> = [
    {
      label: t(lang, 'grpOverview'),
      items: [{ id: 'overview', label: t(lang, 'navOverview'), icon: '◈' }],
    },
    {
      label: t(lang, 'grpStability'),
      items: [{ id: 'stability', label: t(lang, 'navStability'), icon: '△' }],
    },
    {
      label: t(lang, 'grpPerformance'),
      items: [{ id: 'fluency', label: t(lang, 'navFluency'), icon: '◷' }],
    },
    {
      label: t(lang, 'grpNetRes'),
      items: [{ id: 'resources', label: t(lang, 'navNetRes'), icon: '⇄' }],
    },
    {
      label: t(lang, 'grpDiagGov'),
      items: [
        { id: 'diagnostics', label: t(lang, 'navDiagnostics'), icon: '✦' },
        { id: 'operations', label: t(lang, 'navOperations'), icon: '⚙' },
      ],
    },
  ];

  const hasActiveSubFilters = Boolean(appVersion || adapter || scene || deviceModel || osVersion || rangeDays !== 14 || env !== 'prod');

  function resetFilters() {
    setEnv('prod');
    setAppVersion('');
    setAdapter('');
    setScene('');
    setDeviceModel('');
    setOsVersion('');
    setRangeDays(14);
  }

  function copyShareLink() {
    navigator.clipboard?.writeText(window.location.href).then(() => {
      setCopiedLink(true);
      setTimeout(() => setCopiedLink(false), 1800);
    }).catch(() => {});
  }

  async function exportDiagnosticSnapshot() {
    setExporting(true);
    try {
      const [overview, trends, journeys, sourceHealth] = await Promise.all([
        api.overview(filters),
        api.timeseries(filters),
        api.customJourneys(filters),
        api.sourceHealth(filters),
      ]);
      const bundle = {
        exported_at: new Date().toISOString(),
        filters,
        source_health: sourceHealth,
        overview,
        daily_trends: trends.points,
        custom_journeys: journeys.journeys,
      };
      const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `caliper-report-${filters.platform}-${filters.appVersion || 'all'}-${new Date().toISOString().slice(0, 10)}.json`;
      a.click();
      URL.revokeObjectURL(url);
    } finally {
      setExporting(false);
    }
  }

  return (
    <div className="app-frame">
      <header className="global-header">
        <div className="header-left">
          <div className="brand-lockup">
            <div className="brand-mark">C</div>
            <div>
              <div className="brand-name">Caliper APM</div>
            </div>
          </div>
          <span className="brand-divider" />
          <div className="app-pill">
            <strong>{releases[0]?.app_id ?? 'dev.caliper.demo'}</strong>
            <span style={{ color: '#86909c' }}>{t(lang, 'appSub')}</span>
          </div>
          <div className="platform-pills" role="group" aria-label="Platform">
            <button
              className={platform === 'android' ? 'active' : ''}
              onClick={() => { setPlatform('android'); setAppVersion(''); setDeviceModel(''); setOsVersion(''); }}
            >
              Android
            </button>
            <button
              className={platform === 'ios' ? 'active' : ''}
              onClick={() => { setPlatform('ios'); setAppVersion(''); setDeviceModel(''); setOsVersion(''); }}
            >
              iOS
            </button>
          </div>
        </div>

        <div className="header-right">
          <div className="header-stat">
            <span className="health-dot" />
            <span>
              {health
                ? `${health.storage.total_events.toLocaleString('en-US')} ${lang === 'zh' ? '条实时事件' : 'events'}`
                : 'Connecting…'}
            </span>
          </div>
          <span className="brand-divider" />
          <button
            className="lang-btn"
            title={theme === 'dark' ? 'Switch to Light Mode' : 'Switch to Dark Mode'}
            onClick={() => setTheme(th => (th === 'dark' ? 'light' : 'dark'))}
          >
            {theme === 'dark' ? '☀' : '☾'}
          </button>
          <button className="lang-btn" onClick={() => setLang(l => (l === 'zh' ? 'en' : 'zh'))}>
            {lang === 'zh' ? 'EN' : '中文'}
          </button>
        </div>
      </header>

      <div className="workspace-body">
        <aside className="sidebar">
          {navGroups.map(group => (
            <div key={group.label}>
              <div className="nav-group-label">{group.label}</div>
              <nav className="nav-list" aria-label={group.label}>
                {group.items.map(item => (
                  <button
                    key={item.id}
                    className={`nav-item ${tab === item.id ? 'active' : ''}`}
                    onClick={() => setTab(item.id)}
                  >
                    <span className="nav-icon" aria-hidden="true">{item.icon}</span>
                    <span>{item.label}</span>
                  </button>
                ))}
              </nav>
            </div>
          ))}

          <div className="sidebar-engine">
            <div style={{ fontWeight: 600, color: 'var(--text)' }}>Zero-Lie Engine</div>
            <div className="mono">{health?.storage.engine ?? 'caliper-server'}</div>
            <div style={{ color: 'var(--muted)' }}>
              {releases.length} {lang === 'zh' ? '个版本 · 零假数据' : 'releases · real data'}
            </div>
          </div>
        </aside>

        <div className="main-col">
          <div className="filter-bar">
            <div className="filter-group">
              <div className="segmented" role="group" aria-label={t(lang, 'filterRange')}>
                <button className={rangeDays === 7 ? 'active' : ''} onClick={() => setRangeDays(7)}>{t(lang, 'range7d')}</button>
                <button className={rangeDays === 14 ? 'active' : ''} onClick={() => setRangeDays(14)}>{t(lang, 'range14d')}</button>
                <button className={rangeDays === 0 ? 'active' : ''} onClick={() => setRangeDays(0)}>{t(lang, 'rangeAll')}</button>
              </div>

              <label className="filter-chip">
                <span>{t(lang, 'filterEnv')}</span>
                <select value={env} onChange={e => setEnv(e.target.value as EnvName)}>
                  <option value="prod">prod</option>
                  <option value="staging">staging</option>
                  <option value="dev">dev</option>
                </select>
              </label>

              <label className="filter-chip">
                <span>{t(lang, 'filterVersion')}</span>
                <select value={appVersion} onChange={e => setAppVersion(e.target.value)}>
                  <option value="">{t(lang, 'allVersions')}</option>
                  {versions.map(v => <option key={v} value={v}>v{v}</option>)}
                </select>
              </label>

              <label className="filter-chip">
                <span>{lang === 'zh' ? '机型' : 'Device'}</span>
                <select value={deviceModel} onChange={e => setDeviceModel(e.target.value)}>
                  <option value="">{lang === 'zh' ? '全部机型' : 'All Devices'}</option>
                  {dims.devices.map(d => <option key={d} value={d}>{d}</option>)}
                </select>
              </label>

              <label className="filter-chip">
                <span>{lang === 'zh' ? '系统' : 'OS'}</span>
                <select value={osVersion} onChange={e => setOsVersion(e.target.value)}>
                  <option value="">{lang === 'zh' ? '全部系统' : 'All OS'}</option>
                  {dims.os_versions.map(o => <option key={o} value={o}>{o}</option>)}
                </select>
              </label>

              <label className="filter-chip">
                <span>{t(lang, 'filterAdapter')}</span>
                <select value={adapter} onChange={e => setAdapter(e.target.value as AdapterFilter)}>
                  <option value="">{t(lang, 'adapterAll')}</option>
                  <option value="native">{t(lang, 'adapterNative')}</option>
                  <option value="react-native">{t(lang, 'adapterRN')}</option>
                </select>
              </label>

              <label className="filter-chip">
                <span>{t(lang, 'filterScene')}</span>
                <select value={scene} onChange={e => setScene(e.target.value)}>
                  <option value="">{t(lang, 'allScenes')}</option>
                  {dims.scenes.map(s => <option key={s} value={s}>{s}</option>)}
                </select>
              </label>
            </div>

            <div className="filter-group">
              {hasActiveSubFilters && (
                <button className="btn ghost" onClick={resetFilters}>{t(lang, 'reset')}</button>
              )}
              <button className="btn" onClick={copyShareLink}>
                {copiedLink ? (lang === 'zh' ? '已复制链接 ✓' : 'Copied ✓') : (lang === 'zh' ? '分享视图' : 'Share')}
              </button>
              <button className="btn" disabled={exporting} onClick={exportDiagnosticSnapshot}>
                {exporting ? '…' : (lang === 'zh' ? '导出报告' : 'Export JSON')}
              </button>
              <button className="btn" onClick={() => setRefreshTick(n => n + 1)}>{t(lang, 'refresh')}</button>
            </div>
          </div>

          <main className="content">
            {tab === 'overview' && (
              <OverviewPage
                filters={filters}
                releases={releases}
                refreshTick={refreshTick}
                lang={lang}
                onOpenIssue={openIssueDetail}
                onDrillScene={drillToScene}
              />
            )}
            {tab === 'stability' && (
              <StabilityPage
                filters={filters}
                releases={releases}
                refreshTick={refreshTick}
                lang={lang}
                initialFingerprint={focusIssueFP}
                onClearInitialFingerprint={() => setFocusIssueFP(null)}
                onTraceUser={traceUserSession}
                onMutated={() => setRefreshTick(n => n + 1)}
              />
            )}
            {tab === 'fluency' && (
              <FluencyPage filters={filters} refreshTick={refreshTick} lang={lang} />
            )}
            {tab === 'resources' && (
              <ResourcesNetworkPage
                filters={filters}
                refreshTick={refreshTick}
                lang={lang}
                onTraceUser={traceUserSession}
              />
            )}
            {tab === 'diagnostics' && (
              <DiagnosticsPage
                filters={filters}
                releases={releases}
                refreshTick={refreshTick}
                lang={lang}
                initialTarget={focusTraceTarget}
                onClearInitialTarget={() => setFocusTraceTarget(null)}
                onMutated={() => setRefreshTick(n => n + 1)}
              />
            )}
            {tab === 'operations' && (
              <OperationsPage
                filters={filters}
                releases={releases}
                refreshTick={refreshTick}
                lang={lang}
                onMutated={() => setRefreshTick(n => n + 1)}
              />
            )}
          </main>
        </div>
      </div>
    </div>
  );
}
