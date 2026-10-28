import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { EnvironmentDetail } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, user, stubApi, stubSockets } from '../test-utils';
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
  stubSockets();
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
      'GET /environments/1/containers': { body: [] },
      'GET /environments/1/images': { body: [] },
      'GET /environments/1/updates': { body: [] },
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
    expect(wrapper.find('.attention').text()).toContain('Nothing needs attention.');
    expect(wrapper.find('.recent').exists()).toBe(true);
    // The server's own settings are in the top bar, not on its environment.
    expect(wrapper.find('[aria-label="Agent settings"]').exists()).toBe(false);
  });

  it("opens an agent environment's settings from its gear", async () => {
    stubApi({
      'GET /environments/2': { body: { ...env, id: 2, name: 'nas', kind: 'agent' } },
      'GET /environments/2/volumes': { body: [] },
      'GET /environments/2/networks': { body: [] },
      'GET /environments/2/stats': {
        body: { containers: [], total: { cpu_percent: 0, memory_usage: 0, network_rx: 0, network_tx: 0 } },
      },
      'GET /environments/2/containers': { body: [] },
      'GET /environments/2/images': { body: [] },
      'GET /environments/2/updates': { body: [] },
      'GET /environments/2/agent/update': {
        body: { version: 'v0.9.0', status: 'current', can_update: true },
      },
    });
    const wrapper = await mountAt('/env/2');
    await wrapper.find('[aria-label="Agent settings"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('dialog').text()).toContain('Agent of nas');
    expect(wrapper.find('dialog').text()).toContain('v0.9.0');
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
    expect(wrapper.find('.attention').exists()).toBe(false);
  });

  it('shows a user their allowance instead of the host and images', async () => {
    currentUser.value = user;
    const MiB = 1024 * 1024;
    stubApi({
      'GET /environments/1': { body: { ...env, docker: { ...env.docker, images: 0, name: '' } } },
      'GET /environments/1/volumes': { body: [{}] },
      'GET /environments/1/networks': { body: [{}] },
      'GET /environments/1/allowance': {
        body: {
          user: 2,
          limits: {
            cpus_milli: 2000,
            memory_bytes: 2048 * MiB,
            disk_bytes: 0,
            max_containers: 5,
            port_min: 0,
            port_max: 0,
            images: [],
          },
          used: { cpus_milli: 500, memory_bytes: 0, disk_bytes: 0, containers: 1 },
          left: { cpus_milli: 1500, memory_bytes: 2048 * MiB, disk_bytes: 0, containers: 4 },
        },
      },
      'GET /environments/1/stats': {
        body: { containers: [], total: { cpu_percent: 0, memory_usage: 0, network_rx: 0, network_tx: 0 } },
      },
    });
    const wrapper = await mountAt('/env/1');
    expect(wrapper.text()).toContain('Your allowance');
    expect(wrapper.text()).toContain('0.5 / 2');
    expect(wrapper.find('a.primary').attributes('href')).toBe('/env/1/deploy');
    expect(wrapper.findAll('.count .label').map((l) => l.text())).toEqual([
      'Containers',
      'Volumes',
      'Networks',
    ]);
    expect(wrapper.find('.attention').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('Host name');
  });
});
