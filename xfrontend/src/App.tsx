import { useEffect, useState } from 'react';
import { apiCall, BASE } from './api';
import AuthTab from './tabs/Auth';
import CrudTab from './tabs/Crud';
import WsTab from './tabs/Ws';

type TabKey = 'auth' | 'crud' | 'ws';

export default function App() {
  const [tab, setTab] = useState<TabKey>('auth');
  const [health, setHealth] = useState<string>('…');

  useEffect(() => {
    let cancelled = false;
    const check = async () => {
      const r = await apiCall<{ status: string; nats: string }>('/health');
      if (cancelled) return;
      setHealth(r.ok ? `ok (nats: ${r.data?.nats ?? '?'})` : `down (${r.status})`);
    };
    check();
    const t = setInterval(check, 5000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  return (
    <div className="app">
      <header>
        <h1>platform.go — x-services tester</h1>
        <div className="meta">
          <span>Gateway: <code>{BASE || '(proxy)'}</code></span>
          <span>Health: <code>{health}</code></span>
        </div>
        <nav>
          <button className={tab === 'auth' ? 'active' : ''} onClick={() => setTab('auth')}>xauth</button>
          <button className={tab === 'crud' ? 'active' : ''} onClick={() => setTab('crud')}>xhttp</button>
          <button className={tab === 'ws' ? 'active' : ''} onClick={() => setTab('ws')}>xws</button>
        </nav>
      </header>
      <main>
        {tab === 'auth' && <AuthTab />}
        {tab === 'crud' && <CrudTab />}
        {tab === 'ws' && <WsTab />}
      </main>
    </div>
  );
}
