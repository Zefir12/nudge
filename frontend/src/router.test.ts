import { flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { listTokens, setUnauthorizedHandler } from './api/client';
import { page } from './reconnect';
import { createAppRouter, isChunkLoadError, redirectOnUnauthorized } from './router';
import { apiError, currentUser } from './session';
import { admin, stubApi, user, type Routes } from './test-utils';

beforeEach(() => {
  currentUser.value = null;
});

afterEach(() => {
  vi.restoreAllMocks();
  sessionStorage.clear();
  vi.unstubAllGlobals();
  setUnauthorizedHandler(null);
});

const unauthorized = { status: 401, body: { error: 'authentication required' } };

describe('router guard', () => {
  it.each<{ name: string; routes: Routes; to: string; want: string }>([
    {
      name: 'fresh install goes to setup',
      routes: { 'GET /auth/me': unauthorized, 'GET /setup': { body: { needed: true } } },
      to: '/tokens',
      want: '/setup',
    },
    {
      name: 'logged out goes to login with a redirect',
      routes: { 'GET /auth/me': unauthorized, 'GET /setup': { body: { needed: false } } },
      to: '/tokens?x=1',
      want: '/login?redirect=/tokens?x=1',
    },
    {
      name: 'logged out at / goes to login without a redirect',
      routes: { 'GET /auth/me': unauthorized, 'GET /setup': { body: { needed: false } } },
      to: '/',
      want: '/login',
    },
    {
      name: 'logged in passes',
      routes: { 'GET /auth/me': { body: admin } },
      to: '/tokens',
      want: '/tokens',
    },
    {
      name: "a user's home is the deployments",
      routes: { 'GET /auth/me': { body: user } },
      to: '/',
      want: '/deployments',
    },
    {
      // App shows the "not allowed" state; the URL stays.
      name: 'a user reaches an admin page',
      routes: { 'GET /auth/me': { body: user } },
      to: '/registries',
      want: '/registries',
    },
    {
      name: 'the old environments page goes to the dashboard',
      routes: { 'GET /auth/me': { body: admin } },
      to: '/environments',
      want: '/',
    },
    {
      name: 'setup after setup goes to login',
      routes: { 'GET /setup': { body: { needed: false } } },
      to: '/setup',
      want: '/login',
    },
    {
      name: 'login before setup goes to setup',
      routes: { 'GET /setup': { body: { needed: true } } },
      to: '/login',
      want: '/setup',
    },
  ])('$name', async ({ routes, to, want }) => {
    stubApi(routes);
    const router = createAppRouter(createMemoryHistory());
    await router.push(to);
    expect(decodeURIComponent(router.currentRoute.value.fullPath)).toBe(want);
  });

  it('sends a logged-in user away from login', async () => {
    currentUser.value = admin;
    stubApi({});
    const router = createAppRouter(createMemoryHistory());
    await router.push('/login');
    expect(router.currentRoute.value.name).toBe('dashboard');
  });
});

describe('router guard without the API', () => {
  it('stops and reports the error', async () => {
    stubApi({ 'GET /auth/me': { status: 502, body: { error: 'Bad Gateway' } } });
    const router = createAppRouter(createMemoryHistory());
    await router.push('/tokens');
    expect(router.currentRoute.value.matched).toHaveLength(0);
    expect(apiError.value).toBe('Bad Gateway');

    stubApi({ 'GET /auth/me': { body: admin } });
    await router.push('/tokens');
    expect(router.currentRoute.value.name).toBe('tokens');
    expect(apiError.value).toBe('');
  });
});

describe('401 redirect', () => {
  it('sends the user to login and back to where they were', async () => {
    stubApi({ 'GET /auth/me': { body: admin }, 'GET /tokens': unauthorized });
    const router = createAppRouter(createMemoryHistory());
    redirectOnUnauthorized(router);
    await router.push('/tokens');
    stubApi({ 'GET /tokens': unauthorized, 'GET /setup': { body: { needed: false } } });
    await expect(listTokens()).rejects.toMatchObject({ status: 401 });
    await flushPromises();
    expect(currentUser.value).toBeNull();
    expect(router.currentRoute.value.name).toBe('login');
    expect(router.currentRoute.value.query.redirect).toBe('/tokens');
  });
});

describe('lazy views', () => {
  it('loads a view on navigation', async () => {
    stubApi({ 'GET /auth/me': { body: admin } });
    const router = createAppRouter(createMemoryHistory());
    await router.push('/env/local/stacks/web');
    expect(router.currentRoute.value.name).toBe('stack');
    expect(router.currentRoute.value.matched[0]?.components?.default).toHaveProperty('setup');
  });

  it('reloads the page once when a chunk is gone', async () => {
    stubApi({ 'GET /auth/me': { body: admin } });
    const load = vi.spyOn(page, 'load').mockImplementation(() => {});
    const router = createAppRouter(createMemoryHistory());
    let fail = 2;
    router.addRoute({
      path: '/broken',
      component: () =>
        fail-- > 0
          ? Promise.reject(new TypeError('Failed to fetch dynamically imported module: /assets/X-abc.js'))
          : Promise.resolve({ template: '<p/>' }),
    });

    await expect(router.push('/broken')).rejects.toThrow(TypeError);
    expect(load).toHaveBeenCalledExactlyOnceWith('/broken');

    // Still missing after the reload: no reload loop.
    await expect(router.push('/broken')).rejects.toThrow(TypeError);
    expect(load).toHaveBeenCalledOnce();

    // Loaded at last: a later update reloads again.
    await router.push('/broken');
    expect(router.currentRoute.value.path).toBe('/broken');
    expect(sessionStorage.length).toBe(0);
  });

  it.each([
    { err: new TypeError('Failed to fetch dynamically imported module: /a.js'), want: true },
    { err: new TypeError('error loading dynamically imported module: /a.js'), want: true },
    { err: new TypeError('Importing a module script failed.'), want: true },
    { err: new Error('Unable to preload CSS for /assets/a.css'), want: true },
    { err: new Error('Bad Gateway'), want: false },
    { err: 'Failed to fetch dynamically imported module', want: false },
  ])('isChunkLoadError($err) is $want', ({ err, want }) => {
    expect(isChunkLoadError(err)).toBe(want);
  });
});
