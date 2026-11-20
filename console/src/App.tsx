import { useEffect, useState } from 'react';
import { api } from './api.ts';
import { OverviewPage } from './pages/OverviewPage.tsx';
import { StabilityPage } from './pages/StabilityPage.tsx';
import { FluencyPage } from './pages/FluencyPage.tsx';
import { ResourcesNetworkPage } from './pages/ResourcesNetworkPage.tsx';
import { OperationsPage } from './pages/OperationsPage.tsx';
import type { HealthResponse, Platform, ReleaseRecord } from './types.ts';

type Tab = 'overview' | 'stability' | 'fluency' | 'resources' | 'operations';

const TABS: Array<{ id: Tab; label: string; icon: string }> = [
  { id: 'overview', label: 'Overview & Compare', icon: '◈' },
  { id: 'stability', label: 'Stability & Stacks', icon: '△' },
  { id: 'fluency', label: 'Startup & Fluency', icon: '◷' },
  { id: 'resources', label: 'Resources & Network', icon: '⇄' },
  { id: 'operations', label: 'Releases, Symbols & Alerts', icon: '⚙' },
];

export function App() {
  const [tab, setTab] = useState<Tab>('overview');
  const [platform, setPlatform] = useState<Platform>('android');
  const [appVersion, setAppVersion] = useState('');
  const [releases, setReleases] = useState<ReleaseRecord[]>([]);
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);

  useEffect(() => {
    let active = true;
    api.health().then(h => { if (active) setHealth(h); }).catch(() => {});
    api.releases(platform)
      .then(res => { if (active) setReleases(res.releases ?? []); })
      .catch(() => { if (active) setReleases([]); });
    return () => { active = false; };
  }, [platform, refreshTick]);

  const versions = Array.from(new Set(releases.map(r => r.app_version))).sort();

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">C</div>
          <div>
            <div className="brand-title">Caliper APM</div>
            <span className="brand-sub">Native-First · Zero-Lie</span>
          </div>
        </div>

        <nav className="nav" aria-label="Primary Navigation">
          {TABS.map(item => (
            <button key={item.id} className={tab === item.id ? 'active' : ''} onClick={() => setTab(item.id)}>
              <span aria-hidden="true">{item.icon}</span>
              <span>{item.label}</span>
            </button>
          ))}
        </nav>

        <div className="sidebar-footer">
          <div>
            <span className="health-dot" />
            {health ? `${health.storage.total_events.toLocaleString('en-US')} events` : 'Connecting…'}
          </div>
          <div className="mono">{health?.storage.engine ?? 'caliper-server'}</div>
        </div>
      </aside>

      <div className="main">
        <header className="topbar">
          <div className="topbar-title">{TABS.find(t => t.id === tab)?.label}</div>
          <div className="controls">
            <div className="segmented" role="group" aria-label="Platform">
              <button className={platform === 'android' ? 'active' : ''} onClick={() => { setPlatform('android'); setAppVersion(''); }}>Android</button>
              <button className={platform === 'ios' ? 'active' : ''} onClick={() => { setPlatform('ios'); setAppVersion(''); }}>iOS</button>
            </div>
            <select aria-label="App Version" value={appVersion} onChange={e => setAppVersion(e.target.value)}>
              <option value="">All versions</option>
              {versions.map(v => <option key={v} value={v}>v{v}</option>)}
            </select>
            <button className="btn" onClick={() => setRefreshTick(t => t + 1)}>Refresh</button>
          </div>
        </header>

        <main className="content">
          {tab === 'overview' && <OverviewPage platform={platform} appVersion={appVersion} releases={releases} refreshTick={refreshTick} />}
          {tab === 'stability' && <StabilityPage platform={platform} appVersion={appVersion} refreshTick={refreshTick} />}
          {tab === 'fluency' && <FluencyPage platform={platform} appVersion={appVersion} refreshTick={refreshTick} />}
          {tab === 'resources' && <ResourcesNetworkPage platform={platform} appVersion={appVersion} refreshTick={refreshTick} />}
          {tab === 'operations' && <OperationsPage platform={platform} releases={releases} refreshTick={refreshTick} onMutated={() => setRefreshTick(t => t + 1)} />}
        </main>
      </div>
    </div>
  );
}
