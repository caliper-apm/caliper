import { useEffect, useMemo, useState } from 'react';
import { api } from './api.ts';
import { t, type Lang } from './i18n.ts';
import { OverviewPage } from './pages/OverviewPage.tsx';
import { StabilityPage } from './pages/StabilityPage.tsx';
import { FluencyPage } from './pages/FluencyPage.tsx';
import { ResourcesNetworkPage } from './pages/ResourcesNetworkPage.tsx';
import { DiagnosticsPage } from './pages/DiagnosticsPage.tsx';
import { OperationsPage } from './pages/OperationsPage.tsx';
import type { AdapterFilter, EnvName, GlobalFilters, HealthResponse, Platform, ReleaseRecord } from './types.ts';

type Tab = 'overview' | 'stability' | 'fluency' | 'resources' | 'diagnostics' | 'operations';

const SCENES = ['home_feed', 'media_gallery', 'streaming_reader', 'article_detail', 'settings'];

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
  const [lang, setLang] = useState<Lang>('zh');
  const [tab, setTab] = useState<Tab>('overview');
  const [platform, setPlatform] = useState<Platform>('android');
  const [env, setEnv] = useState<EnvName>('prod');
  const [appVersion, setAppVersion] = useState('');
  const [adapter, setAdapter] = useState<AdapterFilter>('');
  const [scene, setScene] = useState('');
  const [rangeDays, setRangeDays] = useState<0 | 7 | 14>(14);
  const [latestDay, setLatestDay] = useState<string | undefined>(undefined);

  const [releases, setReleases] = useState<ReleaseRecord[]>([]);
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);
  const [focusIssueFP, setFocusIssueFP] = useState<string | null>(null);
  const [focusTraceTarget, setFocusTraceTarget] = useState<{ targetType: 'user_hash' | 'install_hash'; hash: string } | null>(null);

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
    api.releases(platform, env)
      .then(res => {
        if (!active) return;
        const list = res.releases ?? [];
        setReleases(list);
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
    return { platform, env, appVersion, adapter, scene, rangeDays, startDayUtc, endDayUtc };
  }, [platform, env, appVersion, adapter, scene, rangeDays, latestDay]);

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

  const hasActiveSubFilters = Boolean(appVersion || adapter || scene || rangeDays !== 14 || env !== 'prod');

  function resetFilters() {
    setEnv('prod');
    setAppVersion('');
    setAdapter('');
    setScene('');
    setRangeDays(14);
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
              onClick={() => { setPlatform('android'); setAppVersion(''); }}
            >
              Android
            </button>
            <button
              className={platform === 'ios' ? 'active' : ''}
              onClick={() => { setPlatform('ios'); setAppVersion(''); }}
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
                  {SCENES.map(s => <option key={s} value={s}>{s}</option>)}
                </select>
              </label>
            </div>

            <div className="filter-group">
              {filters.startDayUtc && (
                <span className="mono" style={{ color: 'var(--muted)' }}>
                  {filters.startDayUtc} ~ {filters.endDayUtc}
                </span>
              )}
              {hasActiveSubFilters && (
                <button className="btn ghost" onClick={resetFilters}>{t(lang, 'reset')}</button>
              )}
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
              <ResourcesNetworkPage filters={filters} refreshTick={refreshTick} lang={lang} />
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
