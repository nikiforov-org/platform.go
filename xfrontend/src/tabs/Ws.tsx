import { useEffect, useRef, useState } from 'react';
import { wsURL } from '../api';

type LogLine = { kind: 'in' | 'out' | 'sys' | 'err'; text: string; ts: string };

export default function WsTab() {
  const [status, setStatus] = useState<'disconnected' | 'connecting' | 'connected'>('disconnected');
  const [text, setText] = useState('hello');
  const [log, setLog] = useState<LogLine[]>([]);
  const wsRef = useRef<WebSocket | null>(null);
  const logEnd = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    logEnd.current?.scrollIntoView({ block: 'end' });
  }, [log]);

  useEffect(() => () => wsRef.current?.close(), []);

  const push = (kind: LogLine['kind'], text: string) =>
    setLog((l) => [...l, { kind, text, ts: new Date().toLocaleTimeString() }]);

  const connect = () => {
    if (wsRef.current) return;
    const url = wsURL('/v1/xws/ws');
    push('sys', `connecting to ${url}`);
    setStatus('connecting');
    const ws = new WebSocket(url);
    wsRef.current = ws;
    ws.onopen = () => {
      setStatus('connected');
      push('sys', 'open');
    };
    ws.onmessage = (ev) => push('in', typeof ev.data === 'string' ? ev.data : '[binary]');
    ws.onerror = () => push('err', 'ws error');
    ws.onclose = (ev) => {
      setStatus('disconnected');
      push('sys', `closed: code=${ev.code} reason=${ev.reason || '—'}`);
      wsRef.current = null;
    };
  };

  const send = (payload: unknown) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      push('err', 'not connected');
      return;
    }
    const data = JSON.stringify(payload);
    ws.send(data);
    push('out', data);
  };

  const disconnect = () => {
    send({ type: 'disconnect' });
  };

  const hardClose = () => {
    wsRef.current?.close();
  };

  return (
    <section>
      <h2>xws — WebSocket</h2>
      <div className="row">
        <span>статус: <code>{status}</code></span>
        <button className="primary" disabled={status !== 'disconnected'} onClick={connect}>connect</button>
        <button className="secondary" disabled={status !== 'connected'} onClick={() => send({ type: 'ping' })}>
          ping
        </button>
        <button className="secondary" disabled={status !== 'connected'} onClick={disconnect}>
          disconnect (graceful)
        </button>
        <button className="secondary" disabled={status === 'disconnected'} onClick={hardClose}>
          close (hard)
        </button>
      </div>
      <div className="row">
        <input
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="text"
          style={{ flex: 1 }}
        />
        <button
          className="primary"
          disabled={status !== 'connected'}
          onClick={() => send({ type: 'message', text })}
        >
          send
        </button>
      </div>
      <div className="log">
        {log.map((l, i) => (
          <div key={i} className={l.kind}>
            <span style={{ color: '#555' }}>[{l.ts}]</span>{' '}
            <span>{l.kind.toUpperCase()}</span> {l.text}
          </div>
        ))}
        <div ref={logEnd} />
      </div>
      <div className="row" style={{ marginTop: 8 }}>
        <button className="secondary" onClick={() => setLog([])}>clear log</button>
      </div>
    </section>
  );
}
