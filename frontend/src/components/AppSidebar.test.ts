import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { environments } from '../environments';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
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
      'Environments',
      'Deployments',
      'Registries',
      'Notifications',
      'API tokens',
      'Audit log',
    ]);
    const envNav = wrapper.find('nav.env');
    expect(envNav.find('.group').text()).toBe('local');
    expect(envNav.find('a[href="/env/1/images"]').exists()).toBe(true);
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
