import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import {
  ApiError,
  apiFetch,
  deleteToken,
  downStack,
  getHealth,
  getMe,
  logout,
  setUnauthorizedHandler,
  upStack,
} from './client';

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

describe('stack runs', () => {
  it('streams compose output and resolves with the exit code', async () => {
    const fn = mockFetch(
      200,
      '{"stream":"stderr","line":" Container web-app-1  Started"}\n{"stream":"stdout","line":"done"}\n{"exit_code":1}\n',
    );
    const lines: string[] = [];
    await expect(upStack('1', 'web', true, (l) => lines.push(`${l.stream}: ${l.line}`))).resolves.toBe(1);
    expect(lines).toEqual(['stderr:  Container web-app-1  Started', 'stdout: done']);
    expect(fn).toHaveBeenCalledWith(
      '/api/v1/environments/1/stacks/web/up',
      expect.objectContaining({ method: 'POST', body: '{"pull":true}' }),
    );
  });

  it('throws on an error line', async () => {
    mockFetch(200, '{"stream":"stdout","line":"pulling"}\n{"error":"compose did not finish"}\n');
    await expect(downStack('1', 'web', false, () => {})).rejects.toEqual(
      new ApiError(502, 'compose did not finish'),
    );
  });

  it('throws when the stream ends without an exit code', async () => {
    mockFetch(200, '{"stream":"stdout","line":"pulling"}\n');
    await expect(upStack('1', 'web', false, () => {})).rejects.toEqual(
      new ApiError(502, 'the compose up ended before it finished'),
    );
  });

  it('throws the error of a run that could not start', async () => {
    mockFetch(409, '{"error":"the stack is being changed"}');
    await expect(upStack('1', 'web', false, () => {})).rejects.toEqual(
      new ApiError(409, 'the stack is being changed'),
    );
  });
});
