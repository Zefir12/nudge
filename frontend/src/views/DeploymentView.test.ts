import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import DeploymentView from './DeploymentView.vue';

const all = ['view', 'deploy', 'edit', 'manage_tokens', 'manage_members', 'delete'];
const shop = (can: string[] = all, role = 'owner') => ({
  name: 'shop',
  environment: 1,
  stack: 'web',
  description: '',
  variables: ['API_IMAGE'],
  created_at: '2026-10-26T12:00:00Z',
  updated_at: '2026-10-26T12:00:00Z',
  role,
  can,
});
const run = (id: number, status: string) => ({
  id,
  actor: 'ci',
  actor_kind: 'token',
  status,
  variables: { API_IMAGE: 'app:2' },
  previous: {},
  queued_at: '2026-10-26T12:00:00Z',
  started_at: null,
  finished_at: null,
  image_before: '',
  image_after: 'app=sha256:2',
  error: status === 'failed' ? 'compose exited with code 1' : '',
});
const member = { user_id: 1, username: 'admin', role: 'owner' };

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/deployments/shop');
  const wrapper = mount(DeploymentView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

const button = (wrapper: ReturnType<typeof mount>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text);

const posts = (fetch: ReturnType<typeof stubApi>, path: string) =>
  fetch.mock.calls.filter(
    ([url, init]) => url === `/api/v1${path}` && (init as RequestInit | undefined)?.method === 'POST',
  );

describe('DeploymentView', () => {
  it('shows runs and deploys with the values set', async () => {
    let runs = [run(1, 'failed')];
    const fetch = stubApi({
      'GET /deployments/shop': { body: shop() },
      'GET /deployments/shop/deploys': () => ({ body: runs }),
      'GET /deployments/shop/members': { body: [member] },
      'GET /deployments/shop/tokens': { body: [] },
      'POST /deployments/shop/deploys': () => {
        runs = [run(2, 'succeeded'), ...runs];
        return { status: 202, body: run(2, 'queued') };
      },
    });
    const { wrapper } = await mountView();
    expect(wrapper.text()).toContain('compose exited with code 1');
    await wrapper.find('input[name="API_IMAGE"]').setValue('app:3');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    const sent = posts(fetch, '/deployments/shop/deploys');
    expect(sent).toHaveLength(1);
    expect(JSON.parse(String((sent[0]?.[1] as RequestInit | undefined)?.body))).toEqual({
      variables: { API_IMAGE: 'app:3' },
    });
    expect(wrapper.findAll('tbody tr')[0]?.text()).toContain('succeeded');
  });

  it('creates a deploy token and shows it once with a CI step', async () => {
    let tokens: unknown[] = [];
    const created = {
      id: 1,
      name: 'ci',
      prefix: 'nudge_dt_ABCD',
      scopes: ['deploy', 'runs:read'],
      created_by: 'admin',
      created_at: '2026-10-26T12:00:00Z',
      expires_at: null,
      last_used_at: null,
      token: 'nudge_dt_ABCDsecret',
    };
    const fetch = stubApi({
      'GET /deployments/shop': { body: shop() },
      'GET /deployments/shop/deploys': { body: [] },
      'GET /deployments/shop/members': { body: [member] },
      'GET /deployments/shop/tokens': () => ({ body: tokens }),
      'POST /deployments/shop/tokens': () => {
        tokens = [created];
        return { status: 201, body: created };
      },
    });
    const { wrapper } = await mountView();
    await wrapper.find('input[name="token-name"]').setValue('ci');
    // The forms: deploy, token, member, settings.
    await wrapper.findAll('form')[1]?.trigger('submit');
    await flushPromises();
    expect(posts(fetch, '/deployments/shop/tokens')).toHaveLength(1);
    expect(wrapper.find('.secret').text()).toBe('nudge_dt_ABCDsecret');
    expect(wrapper.find('.snippet').text()).toContain('/api/v1/deployments/shop/deploys?wait=1');
    expect(wrapper.find('.snippet').text()).toContain('"API_IMAGE":"<api_image>"');
    await button(wrapper, 'Done')?.trigger('click');
    expect(wrapper.find('.secret').exists()).toBe(false);
    expect(wrapper.text()).toContain('nudge_dt_ABCD…');
  });

  it('shows a viewer only what a viewer may do', async () => {
    const fetch = stubApi({
      'GET /deployments/shop': { body: shop(['view'], 'viewer') },
      'GET /deployments/shop/deploys': { body: [run(1, 'succeeded')] },
      'GET /deployments/shop/members': { body: [member] },
    });
    const { wrapper } = await mountView();
    expect(button(wrapper, 'Deploy')).toBeUndefined();
    expect(button(wrapper, 'Create token')).toBeUndefined();
    expect(button(wrapper, 'Add member')).toBeUndefined();
    expect(button(wrapper, 'Delete deployment')).toBeUndefined();
    expect(fetch.mock.calls.some(([url]) => String(url).endsWith('/tokens'))).toBe(false);
  });

  it('deletes after confirming and goes back to the list', async () => {
    stubApi({
      'GET /deployments/shop': { body: shop() },
      'GET /deployments/shop/deploys': { body: [] },
      'GET /deployments/shop/members': { body: [member] },
      'GET /deployments/shop/tokens': { body: [] },
      'DELETE /deployments/shop': { status: 204 },
      'GET /deployments': { body: [] },
      'GET /environments': { body: [] },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper, router } = await mountView();
    await button(wrapper, 'Delete deployment')?.trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('deployments');
  });
});
