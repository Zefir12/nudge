import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { Environment } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import EnvironmentTile from './EnvironmentTile.vue';

const base: Environment = {
  id: 3,
  name: 'prod',
  kind: 'agent',
  online: true,
  created_at: '',
  last_seen_at: null,
};

afterEach(() => {
  vi.unstubAllGlobals();
  currentUser.value = null;
});

async function mountTile(env: Environment) {
  const router = createAppRouter(createMemoryHistory());
  const wrapper = mount(EnvironmentTile, { props: { env }, global: { plugins: [router] } });
  await flushPromises();
  return Object.assign(wrapper, { router });
}

const gaugeLabels = (wrapper: Awaited<ReturnType<typeof mountTile>>) =>
  wrapper.findAll('.gauge svg[role="img"]').map((g) => g.attributes('aria-label'));

const docker = {
  version: '27.3.1',
  os_type: 'linux',
  architecture: 'aarch64',
  ncpu: 4,
  mem_total: 1024 ** 3,
  containers: 5,
  containers_running: 3,
  images: 9,
};

describe('EnvironmentTile', () => {
  it('shows the Docker host of an online environment', async () => {
    const env = { ...base, agent: { enrolled: true, version: '0.9.0' } };
    stubApi({ 'GET /environments/3': { body: { ...env, docker } } });
    const wrapper = await mountTile(env);
    expect(wrapper.find('h2 a').attributes('href')).toBe('/env/3');
    expect(wrapper.find('.address').text()).toMatch(/^agent 0\.9\.0/);
    expect(wrapper.find('.plug').classes()).toContain('on');
    expect(wrapper.find('.stats').text()).toContain('3/5');
    expect(wrapper.find('.stats').text()).toContain('9');
    expect(wrapper.find('.engine').text()).toBe('Docker 27.3.1 · linux/aarch64');
    expect(wrapper.find('.status').text()).toBe('Online');
    // The summary, volumes and stacks are not stubbed (599): no load, dashes for the counts.
    expect(gaugeLabels(wrapper).slice(0, 2)).toEqual(['CPU unknown', 'Memory unknown']);
    expect(wrapper.find('.stats').text()).toContain('Volumes–');
  });

  it('counts volumes, and stacks for admins', async () => {
    currentUser.value = admin;
    stubApi({
      'GET /environments/3': { body: { ...base, docker } },
      'GET /environments/3/volumes': { body: [{ name: 'a' }, { name: 'b' }] },
      'GET /environments/3/stacks': { body: [{ name: 'web' }] },
    });
    const wrapper = await mountTile(base);
    const stats = wrapper.findAll('.stats dd').map((d) => d.text());
    expect(stats).toEqual(['3/5', '9', '2', '1']);
  });

  it('does not ask for stacks for other users', async () => {
    const fetch = stubApi({ 'GET /environments/3': { body: { ...base, docker } } });
    await mountTile(base);
    expect(fetch.mock.calls.map(([url]) => url)).not.toContain('/api/v1/environments/3/stacks');
  });

  it('opens the environment from anywhere on the tile but the settings button', async () => {
    stubApi({ 'GET /environments/3': { body: { ...base, docker } } });
    const wrapper = await mountTile(base);
    const push = vi.spyOn(wrapper.router, 'push');
    await wrapper.find('.gear').trigger('click');
    expect(push).not.toHaveBeenCalled();
    await wrapper.find('.stats').trigger('click');
    expect(push).toHaveBeenCalledWith({ name: 'env', params: { env: '3' } });
  });

  // CPU is against every CPU (200% of one CPU on 4 is half), memory against the host's.
  it('shows load gauges from the summary', async () => {
    stubApi({
      'GET /environments/3': { body: { ...base, docker } },
      'GET /environments/3/stats/summary': {
        body: { sampled_at: '', cpu_percent: 200, memory_usage: 256 * 1024 ** 2, containers: 3 },
      },
    });
    const wrapper = await mountTile(base);
    expect(gaugeLabels(wrapper).slice(0, 2)).toEqual(['CPU 50%', 'Memory 25%']);
    const details = wrapper.findAll('.gauge .detail').map((d) => d.text());
    expect(details[0]).toBe('4 cores');
    expect(details[1]).toContain('/');
  });

  // The charts come from the stored history at once, scaled like the gauges.
  it('charts the stored usage history', async () => {
    const point = (cpu: number, mem: number) => ({
      time: '',
      cpu_percent: cpu,
      cpu_max: cpu,
      memory_usage: mem,
      memory_max: mem,
      network_rx: 0,
      network_tx: 0,
      block_read: 0,
      block_write: 0,
    });
    const fetch = stubApi({
      'GET /environments/3': { body: { ...base, docker } },
      'GET /environments/3/metrics?since=24h&resolution=15m': {
        body: { source: 'raw', step: 900, points: [point(0, 0), point(400, 1024 ** 3)] },
      },
    });
    const wrapper = await mountTile(base);
    expect(fetch.mock.calls.map(([url]) => url)).toContain(
      '/api/v1/environments/3/metrics?since=24h&resolution=15m',
    );
    const lines = wrapper.findAll('.charts path.line').map((p) => p.attributes('d'));
    // 400% of one CPU on 4 and all the memory are both 100%: from the bottom to the top.
    expect(lines).toEqual(['M0.00,29.00 L100.00,2.00', 'M0.00,29.00 L100.00,2.00']);
  });

  it('draws no line without a history', async () => {
    stubApi({ 'GET /environments/3': { body: { ...base, docker } } });
    const wrapper = await mountTile(base);
    expect(wrapper.findAll('.charts path.line').map((p) => p.attributes('d'))).toEqual(['', '']);
  });

  it('says when the Docker host cannot be read', async () => {
    stubApi({ 'GET /environments/3': { status: 502, body: { error: 'daemon gone' } } });
    const wrapper = await mountTile(base);
    expect(wrapper.find('.note').text()).toContain('daemon gone');
  });

  // Offline environments are not asked for Docker info (stubApi would answer 599).
  it.each([
    {
      name: 'a disconnected agent',
      env: { ...base, online: false, agent: { enrolled: true, version: '0.8.2' } },
      text: 'Agent not connected',
      status: 'Offline',
    },
    {
      name: 'an agent not enrolled yet',
      env: { ...base, online: false, agent: { enrolled: false, version: '' } },
      text: 'Waiting for the agent',
      status: 'Waiting for agent',
    },
    {
      name: 'an unreachable local socket',
      env: { ...base, kind: 'local' as const, online: false },
      text: 'Docker is not answering',
      status: 'Offline',
    },
  ])('shows $name', async ({ env, text, status }) => {
    const fetch = stubApi({});
    const wrapper = await mountTile(env);
    expect(wrapper.find('.away').text()).toContain(text);
    expect(wrapper.find('.status').text()).toBe(status);
    expect(wrapper.find('.stats').exists()).toBe(false);
    expect(wrapper.find('.gauges').exists()).toBe(false);
    expect(fetch.mock.calls.map(([url]) => url)).not.toContain('/api/v1/environments/3');
  });
});
