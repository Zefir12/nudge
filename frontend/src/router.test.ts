import { flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { listTokens, setUnauthorizedHandler } from './api/client';
import { createAppRouter, redirectOnUnauthorized } from './router';
import { apiError, currentUser } from './session';
import { admin, stubApi, user, type Routes } from './test-utils';

beforeEach(() => {
  currentUser.value = null;
});

afterEach(() => {
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
