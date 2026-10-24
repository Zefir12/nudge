import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import NetworkView from './NetworkView.vue';

const front = {
  id: 'n2abcdef',
  name: 'front',
  driver: 'bridge',
  scope: 'local',
  created_at: null,
  internal: false,
  attachable: false,
  ingress: false,
  ipam: [],
  labels: {},
  options: {},
  containers: [],
};

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('NetworkView', () => {
  it('removes the network by ID after confirming', async () => {
    const fetch = stubApi({
      'GET /environments/1/networks/front': { body: front },
      'DELETE /environments/1/networks/n2abcdef': { status: 204 },
      'GET /environments/1/networks': { body: [] },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const router = createAppRouter(createMemoryHistory());
    await router.push('/env/1/networks/front');
    const wrapper = mount(NetworkView, { global: { plugins: [router] } });
    await flushPromises();
    const remove = wrapper.findAll('button').find((b) => b.text() === 'Remove')!;
    await remove.trigger('click');
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
    confirm.mockReturnValue(true);
    await remove.trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('networks');
  });
});
