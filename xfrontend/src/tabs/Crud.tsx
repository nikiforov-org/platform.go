import { useState } from 'react';
import { apiCall } from '../api';

type Item = {
  id: number;
  name: string;
  value: string;
  created_at: string;
  updated_at: string;
};

export default function CrudTab() {
  const [items, setItems] = useState<Item[]>([]);
  const [name, setName] = useState('');
  const [value, setValue] = useState('');
  const [getId, setGetId] = useState('');
  const [out, setOut] = useState<string>('');
  const [busy, setBusy] = useState(false);

  const show = (label: string, r: unknown) => setOut(`[${label}] ${JSON.stringify(r, null, 2)}`);

  const list = async () => {
    setBusy(true);
    const r = await apiCall<Item[]>('/v1/xhttp/list', { method: 'POST', body: '{}' });
    if (r.ok && Array.isArray(r.data)) setItems(r.data);
    show('list', r);
    setBusy(false);
  };

  const create = async () => {
    setBusy(true);
    const r = await apiCall<Item>('/v1/xhttp/create', {
      method: 'POST',
      body: JSON.stringify({ name, value }),
    });
    show('create', r);
    if (r.ok) {
      setName('');
      setValue('');
      await list();
    }
    setBusy(false);
  };

  const get = async () => {
    setBusy(true);
    const r = await apiCall<Item>('/v1/xhttp/get', {
      method: 'POST',
      body: JSON.stringify({ id: Number(getId) }),
    });
    show('get', r);
    setBusy(false);
  };

  const update = async (it: Item) => {
    const newName = prompt('name', it.name);
    if (newName === null) return;
    const newValue = prompt('value', it.value);
    if (newValue === null) return;
    setBusy(true);
    const r = await apiCall<Item>('/v1/xhttp/update', {
      method: 'POST',
      body: JSON.stringify({ id: it.id, name: newName, value: newValue }),
    });
    show('update', r);
    await list();
    setBusy(false);
  };

  const del = async (id: number) => {
    if (!confirm(`Удалить #${id}?`)) return;
    setBusy(true);
    const r = await apiCall(`/v1/xhttp/delete`, {
      method: 'POST',
      body: JSON.stringify({ id }),
    });
    show('delete', r);
    await list();
    setBusy(false);
  };

  return (
    <section>
      <h2>xhttp — CRUD</h2>

      <div className="row">
        <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
        <input placeholder="value" value={value} onChange={(e) => setValue(e.target.value)} />
        <button className="primary" disabled={busy || !name} onClick={create}>create</button>
      </div>

      <div className="row">
        <button className="secondary" disabled={busy} onClick={list}>list</button>
        <input
          placeholder="id"
          value={getId}
          onChange={(e) => setGetId(e.target.value)}
          style={{ width: 80 }}
        />
        <button className="secondary" disabled={busy || !getId} onClick={get}>get by id</button>
      </div>

      {items.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>id</th>
              <th>name</th>
              <th>value</th>
              <th>updated</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.id}>
                <td>{it.id}</td>
                <td>{it.name}</td>
                <td>{it.value}</td>
                <td>{new Date(it.updated_at).toLocaleString()}</td>
                <td>
                  <button className="secondary" disabled={busy} onClick={() => update(it)}>edit</button>{' '}
                  <button className="secondary" disabled={busy} onClick={() => del(it.id)}>del</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <pre className="out">{out || 'Нажми list, чтобы увидеть записи.'}</pre>
    </section>
  );
}
