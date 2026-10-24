import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { currentUser, loadUser, safeRedirect } from './session';
import { admin, stubApi } from './test-utils';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('safeRedirect', () => {
  it.each([
    [undefined, '/'],
    ['', '/'],
    ['/tokens', '/tokens'],
    ['/tokens?x=1#y', '/tokens?x=1#y'],
    ['//evil.example', '/'],
    ['/\\evil.example', '/'],
    ['https://evil.example', '/'],
    ['javascript:alert(1)', '/'],
    [['/a', '/b'], '/'],
  ])('%j -> %s', (input, want) => {
    expect(safeRedirect(input)).toBe(want);
  });
});

describe('loadUser', () => {
  it('returns the user', async () => {
    stubApi({ 'GET /auth/me': { body: admin } });
    await expect(loadUser()).resolves.toEqual(admin);
    expect(currentUser.value).toEqual(admin);
  });

  it('returns null without a session', async () => {
    stubApi({ 'GET /auth/me': { status: 401, body: { error: 'authentication required' } } });
    await expect(loadUser()).resolves.toBeNull();
  });

  it('throws other errors', async () => {
    stubApi({ 'GET /auth/me': { status: 500, body: { error: 'internal error' } } });
    await expect(loadUser()).rejects.toMatchObject({ status: 500 });
  });
});
