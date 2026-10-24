// Typed wrapper around nudge's /api/v1. Every request goes through apiFetch, so auth and
// error handling live in one place.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

let onUnauthorized: (() => void) | null = null;

// setUnauthorizedHandler registers what happens when a request answers 401 because the
// session ended (the app sends the user to login).
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

export interface FetchOptions {
  // Whether a 401 calls the unauthorized handler (default true). Off for the calls whose
  // 401 means something else: a wrong password, or "not logged in" while checking.
  redirectOn401?: boolean;
}

export async function apiFetch<T>(path: string, init?: RequestInit, opts?: FetchOptions): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    ...init,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Not JSON (a proxy's error page): keep the status text.
    }
    if (res.status === 401 && (opts?.redirectOn401 ?? true)) onUnauthorized?.();
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

const post = (body: unknown): RequestInit => ({ method: 'POST', body: JSON.stringify(body) });

export interface Health {
  status: string;
  version: string;
}

export const getHealth = () => apiFetch<Health>('/health');

export interface User {
  id: number;
  username: string;
  created_at: string;
}

export interface Credentials {
  username: string;
  password: string;
}

export const getSetupStatus = () => apiFetch<{ needed: boolean }>('/setup');
export const setup = (c: Credentials) => apiFetch<User>('/setup', post(c), { redirectOn401: false });
export const login = (c: Credentials) => apiFetch<User>('/auth/login', post(c), { redirectOn401: false });
// The caller navigates to login itself, so a 401 (session already gone) must not redirect too.
export const logout = () => apiFetch<void>('/auth/logout', { method: 'POST' }, { redirectOn401: false });
export const getMe = () => apiFetch<User>('/auth/me', undefined, { redirectOn401: false });

export interface Token {
  id: number;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at: string | null;
}

// CreatedToken carries the secret, which the API returns only once.
export interface CreatedToken extends Token {
  token: string;
}

export const listTokens = () => apiFetch<Token[]>('/tokens');
export const createToken = (name: string) => apiFetch<CreatedToken>('/tokens', post({ name }));
export const deleteToken = (id: number) => apiFetch<void>(`/tokens/${id}`, { method: 'DELETE' });
