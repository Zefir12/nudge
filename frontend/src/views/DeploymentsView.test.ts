import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { environments } from '../environments';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, user } from '../test-utils';
import DeploymentsView from './DeploymentsView.vue';

const local = {
  id: 1,
  name: 'local',
  kind: 'local',
  online: true,
  created_at: '2026-10-24T12:00:00Z',
  last_seen_at: null,
};
const shop = {
  name: 'shop',
  environment: 1,
  stack: 'web',
  description: 'the shop',
  variables: ['API_IMAGE'],
  created_at: '2026-10-26T12:00:00Z',
  updated_at: '2026-10-26T12:00:00Z',
  role: 'owner',
  can: ['view', 'deploy', 'edit', 'manage_tokens', 'manage_members', 'delete'],
};

beforeEach(() => {
  currentUser.value = admin;
  environments.value = null;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/deployments');
  const wrapper = mount(DeploymentsView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

describe('DeploymentsView', () => {
  it('shows a user the list without the create form or environments', async () => {
    currentUser.value = user;
    // No GET /environments stub: a user's view must not ask for them.
    stubApi({ 'GET /deployments': { body: [{ ...shop, role: 'deployer' }] } });
    const { wrapper } = await mountView();
    expect(wrapper.find('form.create').exists()).toBe(false);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(wrapper.find('tbody tr').text()).toContain('deployer');
  });

  it('lists deployments', async () => {
    stubApi({ 'GET /deployments': { body: [shop] }, 'GET /environments': { body: [local] } });
    const { wrapper } = await mountView();
    const row = wrapper.find('tbody tr').text();
    expect(row).toContain('shop');
    expect(row).toContain('local');
    expect(row).toContain('owner');
  });

  it('creates a deployment and opens it', async () => {
    const fetch = stubApi({
      'GET /deployments': { body: [] },
      'GET /environments': { body: [local] },
      'GET /environments/1/stacks': { body: [{ name: 'web', containers: 1, running: 1 }] },
      'POST /deployments': { status: 201, body: shop },
      'GET /deployments/shop': { body: shop },
      'GET /deployments/shop/deploys': { body: [] },
      'GET /deployments/shop/members': { body: [] },
      'GET /deployments/shop/tokens': { body: [] },
    });
    const { wrapper, router } = await mountView();
    await wrapper.find('input[name="name"]').setValue('shop');
    await wrapper.find('select[name="environment"]').setValue(1);
    await flushPromises();
    await wrapper.find('select[name="stack"]').setValue('web');
    await wrapper.find('input[name="variables"]').setValue('API_IMAGE, WORKER_IMAGE');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    const post = fetch.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'POST');
    expect(JSON.parse(String((post?.[1] as RequestInit | undefined)?.body))).toEqual({
      name: 'shop',
      environment: 1,
      stack: 'web',
      description: '',
      variables: ['API_IMAGE', 'WORKER_IMAGE'],
    });
    expect(router.currentRoute.value.fullPath).toBe('/deployments/shop');
  });

  it('shows a create error', async () => {
    stubApi({
      'GET /deployments': { body: [] },
      'GET /environments': { body: [local] },
      'GET /environments/1/stacks': { body: [{ name: 'web', containers: 1, running: 1 }] },
      'POST /deployments': { status: 409, body: { error: 'a deployment with that name already exists' } },
    });
    const { wrapper } = await mountView();
    await wrapper.find('input[name="name"]').setValue('shop');
    await wrapper.find('select[name="environment"]').setValue(1);
    await flushPromises();
    await wrapper.find('select[name="stack"]').setValue('web');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain('already exists');
  });
});
