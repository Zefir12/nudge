import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { ApiError, apiFetch, deleteToken, getHealth, getMe, logout, setUnauthorizedHandler } from './client';

function mockFetch(status: number, body: string) {
  const fn = vi.fn(async () => new Response(status === 204 ? null : body, { status, statusText: 'Status' }));
  vi.stubGlobal('fetch', fn);
  return fn;
}

afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(null);
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

  it('sends cookies and JSON', async () => {
    const fn = mockFetch(200, '{}');
    await apiFetch('/x', { method: 'POST', body: '{}' });
    expect(fn).toHaveBeenCalledWith(
      '/api/v1/x',
      expect.objectContaining({
        credentials: 'same-origin',
        headers: expect.objectContaining({ 'Content-Type': 'application/json' }),
      }),
    );
  });

  it('returns nothing for 204', async () => {
    mockFetch(204, '');
    await expect(deleteToken(1)).resolves.toBeUndefined();
  });

  it('calls the unauthorized handler on 401', async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    mockFetch(401, '{"error":"authentication required"}');
    await expect(apiFetch('/tokens')).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledOnce();
  });

  it('leaves 401 to the caller when asked', async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    mockFetch(401, '{"error":"authentication required"}');
    await expect(getMe()).rejects.toMatchObject({ status: 401 });
    await expect(logout()).rejects.toMatchObject({ status: 401 });
    expect(handler).not.toHaveBeenCalled();
  });
});
