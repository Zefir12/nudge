import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { EnvironmentDetail } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import DashboardView from './DashboardView.vue';

const env: EnvironmentDetail = {
  id: 1,
  name: 'local',
  kind: 'local',
  online: true,
  created_at: '2026-10-01T12:00:00Z',
  last_seen_at: '2026-10-24T12:00:00Z',
  docker: {
    name: 'host1',
    version: '28.5.1',
    api_version: '1.51',
    min_api_version: '1.24',
    os: 'Debian GNU/Linux 13',
    os_type: 'linux',
    kernel_version: '6.8.0',
    architecture: 'x86_64',
    ncpu: 8,
    mem_total: 16e9,
    containers: 5,
    containers_running: 3,
    containers_paused: 0,
    containers_stopped: 2,
    images: 7,
    storage_driver: 'overlay2',
    docker_root_dir: '/var/lib/docker',
  },
};

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(DashboardView, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('DashboardView', () => {
  it('shows host info and counts', async () => {
    stubApi({
      'GET /environments/1': { body: env },
      'GET /environments/1/volumes': { body: [{}, {}] },
      'GET /environments/1/networks': { body: [{}, {}, {}] },
      'GET /environments/1/stats': {
        body: {
          containers: [],
          total: { cpu_percent: 12.5, memory_usage: 2e9, network_rx: 0, network_tx: 0 },
        },
      },
    });
    const wrapper = await mountAt('/env/1');
    const counts = wrapper.findAll('.count').map((c) => c.text());
    expect(counts[0]).toContain('5');
    expect(counts[0]).toContain('3 running · 2 stopped');
    expect(wrapper.findAll('.count .number').map((n) => n.text())).toEqual(['5', '7', '2', '3']);
    expect(wrapper.text()).toContain('28.5.1');
    expect(wrapper.text()).toContain('Debian GNU/Linux 13');
    expect(wrapper.text()).toContain('16.0 GB');
    expect(wrapper.find('.count').attributes('href')).toBe('/env/1/containers');
    expect(wrapper.find('.totals').text()).toContain('12.5% CPU');
  });

  it('shows an offline environment', async () => {
    const offline = { status: 503, body: { error: 'the environment is offline' } };
    stubApi({
      'GET /environments/1': offline,
      'GET /environments/1/volumes': offline,
      'GET /environments/1/networks': offline,
      'GET /environments/1/stats': offline,
    });
    const wrapper = await mountAt('/env/1');
    expect(wrapper.find('[role=alert]').text()).toContain('the environment is offline');
  });
});
