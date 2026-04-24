import { useState } from 'react';
import { apiCall } from '../api';

export default function AuthTab() {
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('dev-password');
  const [out, setOut] = useState<string>('');
  const [busy, setBusy] = useState(false);

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      const r = await fn();
      setOut(`[${label}] ${JSON.stringify(r, null, 2)}`);
    } catch (e) {
      setOut(`[${label}] error: ${String(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const login = () =>
    run('login', () =>
      apiCall('/v1/xauth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      }),
    );

  const me = () => run('me', () => apiCall('/v1/xauth/me', { method: 'POST' }));
  const refresh = () => run('refresh', () => apiCall('/v1/xauth/refresh', { method: 'POST' }));
  const logout = () => run('logout', () => apiCall('/v1/xauth/logout', { method: 'POST' }));

  return (
    <section>
      <h2>xauth — JWT в HttpOnly-куках</h2>
      <div className="row">
        <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="username" />
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="password"
        />
      </div>
      <div className="row">
        <button className="primary" disabled={busy} onClick={login}>login</button>
        <button className="secondary" disabled={busy} onClick={me}>me</button>
        <button className="secondary" disabled={busy} onClick={refresh}>refresh</button>
        <button className="secondary" disabled={busy} onClick={logout}>logout</button>
      </div>
      <pre className="out">{out || 'Нажми кнопку — тут появится ответ.'}</pre>
    </section>
  );
}
