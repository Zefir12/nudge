import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import EnvSwitcher from './EnvSwitcher.vue';

const envs = [
  { id: 1, name: 'local', kind: 'local', online: true, created_at: '', last_seen_at: null },
  { id: 4, name: 'nas', kind: 'agent', online: false, created_at: '', last_seen_at: null },
];

beforeEach(() => {
  currentUser.value = admin;
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('EnvSwitcher', () => {
  it.each([
    { name: 'keeps the list page', from: '/env/1/images', want: '/env/4/images' },
    { name: 'goes from a detail page to its list', from: '/env/1/containers/abc', want: '/env/4/containers' },
    { name: 'keeps the dashboard', from: '/env/1', want: '/env/4' },
    { name: 'opens the dashboard from elsewhere', from: '/tokens', want: '/env/4' },
  ])('$name', async ({ from, want }) => {
    stubApi({ 'GET /environments': { body: envs } });
    const router = createAppRouter(createMemoryHistory());
    await router.push(from);
    const wrapper = mount(EnvSwitcher, { global: { plugins: [router] } });
    await flushPromises();
    const options = wrapper.findAll('option:not([disabled])').map((o) => o.text());
    expect(options).toEqual(['local', 'nas (offline)']);
    // The open environment's status; none outside an environment.
    const badge = wrapper.find('.badge');
    expect(badge.exists() && badge.text()).toBe(from.startsWith('/env/') ? 'online' : false);
    await wrapper.find('select').setValue('4');
    await flushPromises();
    expect(router.currentRoute.value.fullPath).toBe(want);
    expect(localStorage.getItem('nudge.lastEnvironment')).toBe('4');
  });
});
