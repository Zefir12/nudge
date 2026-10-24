// Test helpers: a fetch stub that answers by "METHOD /path" (the part after /api/v1).
import { vi } from 'vite-plus/test';

export interface Reply {
  status?: number;
  body?: unknown;
}

export type Routes = Record<string, Reply | ((init?: RequestInit) => Reply)>;

export function stubApi(routes: Routes) {
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    const key = `${init?.method ?? 'GET'} ${url.replace(/^\/api\/v1/, '')}`;
    const route = routes[key];
    if (!route) return new Response(JSON.stringify({ error: `no stub for ${key}` }), { status: 599 });
    const reply = typeof route === 'function' ? route(init) : route;
    const status = reply.status ?? 200;
    return new Response(status === 204 ? null : JSON.stringify(reply.body ?? {}), { status });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

export const admin = { id: 1, username: 'admin', created_at: '2026-10-24T12:00:00Z' };
