import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { environments } from '../environments';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, user } from '../test-utils';
import AppSidebar from './AppSidebar.vue';

const envs = [{ id: 1, name: 'local', kind: 'local', online: true, created_at: '', last_seen_at: null }];

beforeEach(() => {
  currentUser.value = admin;
  environments.value = null;
  stubApi({ 'GET /environments': { body: envs } });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function mountSidebar(path: string, env: string | null) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(AppSidebar, { props: { env }, global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

const labels = (navs: ReturnType<Awaited<ReturnType<typeof mountSidebar>>['findAll']>) =>
  navs.map((a) => a.text());

describe('AppSidebar', () => {
  it('shows the account, the global pages and the environment pages', async () => {
    const wrapper = await mountSidebar('/env/1/containers', '1');
    expect(wrapper.find('.account').text()).toContain('admin');
    expect(labels(wrapper.findAll('nav[aria-label="Global"] a'))).toEqual([
      'Dashboard',
      'Deployments',
      'Registries',
      'Notifications',
      'API tokens',
      'Users',
      'Audit log',
      'Backup',
    ]);
    const envNav = wrapper.find('nav.env');
    expect(envNav.find('.group').text()).toBe('local');
    expect(envNav.find('a[href="/env/1/images"]').exists()).toBe(true);
  });

  it('shows a user only the pages open to users', async () => {
    currentUser.value = user;
    const wrapper = await mountSidebar('/deployments', '1');
    expect(wrapper.find('.account .role').text()).toBe('User');
    expect(labels(wrapper.findAll('nav[aria-label="Global"] a'))).toEqual(['Deployments', 'API tokens']);
    // Their environment pages: no stacks or images, which stay with admins.
    expect(labels(wrapper.findAll('nav.env a'))).toEqual([
      'Overview',
      'Containers',
      'Volumes',
      'Networks',
      'Events',
    ]);
    expect(wrapper.find('.switcher').exists()).toBe(true);
  });

  it('shows the server version at the bottom', async () => {
    stubApi({
      'GET /environments': { body: envs },
      'GET /health': { body: { status: 'ok', version: 'v1.4.2' } },
    });
    const wrapper = await mountSidebar('/', null);
    expect(wrapper.find('.version').text()).toBe('nudge v1.4.2');
  });

  it('leaves out the version when health fails', async () => {
    const wrapper = await mountSidebar('/', null);
    expect(wrapper.find('.version').exists()).toBe(false);
  });

  it('leaves out the environment part when there is no environment', async () => {
    const wrapper = await mountSidebar('/', null);
    expect(wrapper.find('nav.env').exists()).toBe(false);
  });

  it.each([
    { path: '/', env: null, want: 'Dashboard' },
    { path: '/registries/3', env: null, want: 'Registries' },
    { path: '/env/1', env: '1', want: 'Overview' },
    { path: '/env/1/containers/abc', env: '1', want: 'Containers' },
    { path: '/env/1/new-stack', env: '1', want: 'Stacks' },
    { path: '/env/1/deploy', env: '1', want: 'Containers' },
    { path: '/env/1/containers/abc/redeploy', env: '1', want: 'Containers' },
  ])('marks $want active on $path', async ({ path, env, want }) => {
    const wrapper = await mountSidebar(path, env);
    expect(labels(wrapper.findAll('a[aria-current="page"]'))).toEqual([want]);
  });

  it('asks to log out', async () => {
    const wrapper = await mountSidebar('/', null);
    await wrapper.find('button[aria-label="Log out"]').trigger('click');
    expect(wrapper.emitted('logout')).toHaveLength(1);
  });
});
