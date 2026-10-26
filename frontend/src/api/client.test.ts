import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import {
  ApiError,
  apiFetch,
  deleteRegistryTag,
  deleteToken,
  downStack,
  getHealth,
  getMe,
  getRegistryManifest,
  listRegistryRepositories,
  listRegistryTags,
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

  it('names the status when there is no status text (HTTP/2) or error message', async () => {
    const cases: [string, string][] = [
      ['Method Not Allowed\n', 'Request failed (405)'],
      ['{"error":""}', 'Request failed (405)'],
    ];
    for (const [body, message] of cases) {
      vi.stubGlobal(
        'fetch',
        vi.fn(async () => new Response(body, { status: 405 })),
      );
      await expect(apiFetch('/environments/2', { method: 'DELETE' })).rejects.toMatchObject({
        status: 405,
        message,
      });
    }
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

describe('registry browsing', () => {
  it('puts repository names with slashes in the query', async () => {
    const fn = mockFetch(200, '[]');
    await listRegistryTags(2, 'zefir/nudge');
    await getRegistryManifest(2, 'zefir/nudge', 'v1.2');
    await listRegistryRepositories(2, 'nu', 'a/b');
    mockFetch(204, '');
    await deleteRegistryTag(2, 'zefir/nudge', 'latest', 'sha256:abc');
    expect(fn.mock.calls.map((c) => (c as unknown[])[0])).toEqual([
      '/api/v1/registries/2/tags?repository=zefir%2Fnudge',
      '/api/v1/registries/2/manifest?repository=zefir%2Fnudge&tag=v1.2',
      '/api/v1/registries/2/repositories?q=nu&cursor=a%2Fb',
    ]);
  });

  it('deletes a tag with its digest', async () => {
    const fn = mockFetch(204, '');
    await expect(deleteRegistryTag(2, 'zefir/nudge', 'latest', 'sha256:abc')).resolves.toBeUndefined();
    expect(fn).toHaveBeenCalledWith(
      '/api/v1/registries/2/tags?repository=zefir%2Fnudge&tag=latest&digest=sha256%3Aabc',
      expect.objectContaining({ method: 'DELETE' }),
    );
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
