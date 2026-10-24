import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import NetworksView from './NetworksView.vue';

const net = (name: string) => ({
  id: `id-${name}`,
  name,
  driver: 'bridge',
  scope: 'local',
  created_at: null,
  internal: false,
  attachable: false,
  ingress: false,
  ipam: [],
  labels: {},
});

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('NetworksView', () => {
  it('prunes unused networks and reloads the list', async () => {
    let networks = [net('bridge'), net('old')];
    stubApi({
      'GET /environments/1/networks': () => ({ body: networks }),
      'POST /environments/1/networks/prune': () => {
        networks = [net('bridge')];
        return { body: { deleted: ['old'] } };
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const router = createAppRouter(createMemoryHistory());
    await router.push('/env/1/networks');
    const wrapper = mount(NetworksView, { global: { plugins: [router] } });
    await flushPromises();
    expect(wrapper.findAll('tbody tr')).toHaveLength(2);
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Prune unused')!
      .trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=status]').text()).toBe('Removed old.');
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
  });
});
