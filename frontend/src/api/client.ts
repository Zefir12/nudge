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

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    ...init,
    headers: { Accept: 'application/json', ...init?.headers },
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Not JSON (a proxy's error page): keep the status text.
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}

export interface Health {
  status: string;
  version: string;
}

export const getHealth = () => apiFetch<Health>('/health');
