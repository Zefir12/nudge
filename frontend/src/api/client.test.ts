import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { ApiError, apiFetch, getHealth } from './client';

function mockFetch(status: number, body: string) {
  const fn = vi.fn(async () => new Response(body, { status, statusText: 'Status' }));
  vi.stubGlobal('fetch', fn);
  return fn;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('apiFetch', () => {
  it('prefixes /api/v1 and decodes JSON', async () => {
    const fn = mockFetch(200, '{"status":"ok","version":"v1"}');
    await expect(getHealth()).resolves.toEqual({ status: 'ok', version: 'v1' });
    expect(fn).toHaveBeenCalledWith('/api/v1/health', expect.anything());
  });

  it('throws ApiError with the server message', async () => {
    mockFetch(404, '{"error":"not found"}');
    await expect(apiFetch('/nope')).rejects.toEqual(new ApiError(404, 'not found'));
  });

  it('falls back to the status text when the body is not JSON', async () => {
    mockFetch(502, '<html>bad gateway</html>');
    await expect(apiFetch('/health')).rejects.toMatchObject({ status: 502, message: 'Status' });
  });
});
