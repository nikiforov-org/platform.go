// Базовый URL Gateway. Пустая строка = относительные пути через Vite dev-proxy.
export const BASE = import.meta.env.VITE_GATEWAY_URL ?? '';

export type ApiResult<T> = {
  ok: boolean;
  status: number;
  requestId?: string;
  data?: T;
  error?: string;
};

export async function apiCall<T = unknown>(
  path: string,
  init?: RequestInit,
): Promise<ApiResult<T>> {
  const res = await fetch(`${BASE}${path}`, {
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...(init?.headers || {}) },
    ...init,
  });
  const requestId = res.headers.get('X-Request-Id') || undefined;
  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const err =
      data && typeof data === 'object' && 'error' in (data as Record<string, unknown>)
        ? String((data as Record<string, unknown>).error)
        : typeof data === 'string'
        ? data
        : res.statusText;
    return { ok: false, status: res.status, requestId, error: err };
  }
  return { ok: true, status: res.status, requestId, data: data as T };
}

export function wsURL(path: string): string {
  if (BASE) {
    const u = new URL(path, BASE);
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    return u.toString();
  }
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${path}`;
}
