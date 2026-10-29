import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { Environment } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, stubSockets, type FakeWebSocket } from '../test-utils';
import EnvironmentTile from './EnvironmentTile.vue';

const base: Environment = {
  id: 3,
  name: 'prod',
  kind: 'agent',
  online: true,
  created_at: '',
  last_seen_at: null,
};

let sockets: FakeWebSocket[];

beforeEach(() => {
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
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
  it.each([
    {
      name: 'address and ping',
      agent: { address: '203.0.113.9', rtt_ms: 48.3 },
      text: 'agent 0.9.0 · 203.0.113.9 · 48.3 ms',
      tone: 'good',
    },
    { name: 'ping only (a user)', agent: { rtt_ms: 160 }, text: 'agent 0.9.0 · 160 ms', tone: 'bad' },
    {
      name: 'address before the first ping',
      agent: { address: '203.0.113.9' },
      text: 'agent 0.9.0 · 203.0.113.9',
    },
    { name: 'nothing known', agent: {}, text: 'agent 0.9.0' },
  ])("shows the agent's link: $name", async ({ agent, text, tone }) => {
    const env = { ...base, agent: { enrolled: true, version: '0.9.0', ...agent } };
    stubApi({ 'GET /environments/3': { body: { ...env, docker } } });
    const wrapper = await mountTile(env);
    expect(wrapper.find('.address').text().replace(/\s+/g, ' ')).toBe(text);
    if (tone) expect(wrapper.find('.ping').classes()).toContain(tone);
    else expect(wrapper.find('.ping').exists()).toBe(false);
  });

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

  const part = (size: number, reclaimable = 0, count = 0, active = 0) => ({
    size,
    reclaimable,
    count,
    active,
  });
  const usage = {
    measured_at: '2026-10-27T12:00:00Z',
    volumes: part(40e9, 2e9, 6, 4),
    images: part(20e9, 5e9, 12, 1),
    build_cache: part(3e9, 1e9, 9, 0),
    containers: part(2e9, 0, 3, 3),
  };

  it.each([
    {
      name: 'with the disk size',
      disk: { total: 100e9, free: 25e9 },
      big: '75.0 GB of 100.0 GB used',
      legend: [
        'Volumes 40.0 GB',
        'Images 20.0 GB',
        'Build cache 3.0 GB',
        'Containers 2.0 GB',
        'Other 10.0 GB',
      ],
      gauge: ['Disk 75%', '75.0 GB / 100.0 GB'],
    },
    {
      name: 'without it (no mount, an older agent)',
      disk: null,
      big: '65.0 GB used by Docker',
      legend: ['Volumes 40.0 GB', 'Images 20.0 GB', 'Build cache 3.0 GB', 'Containers 2.0 GB'],
      gauge: ['Disk unknown', '65.0 GB Docker'],
    },
  ])("shows an admin the disk's usage $name", async ({ disk, big, legend, gauge }) => {
    currentUser.value = admin;
    stubApi({
      'GET /environments/3': { body: { ...base, docker } },
      'GET /environments/3/disk': { body: { usage, disk } },
    });
    const wrapper = await mountTile(base);
    expect(wrapper.find('.big').text()).toBe(big);
    expect(wrapper.findAll('.legend li').map((l) => l.text())).toEqual(legend);
    expect(wrapper.find('.reclaim').text()).toBe(
      '8.0 GB reclaimable: 11 unused images, 2 unused volumes, build cache',
    );
    expect(gaugeLabels(wrapper)[2]).toBe(gauge[0]);
    expect(wrapper.findAll('.gauge .detail').map((d) => d.text())[2]).toBe(gauge[1]);
  });

  it('shows no disk bar before the first measurement', async () => {
    currentUser.value = admin;
    stubApi({
      'GET /environments/3': { body: { ...base, docker } },
      'GET /environments/3/disk': { body: { usage: null, disk: { total: 100e9, free: 60e9 } } },
    });
    const wrapper = await mountTile(base);
    expect(wrapper.find('.big').exists()).toBe(false);
    expect(gaugeLabels(wrapper)[2]).toBe('Disk 40%');
  });

  it('shows the counted events per hour in the heatmap', async () => {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const days = [
      '2026-10-21',
      '2026-10-22',
      '2026-10-23',
      '2026-10-24',
      '2026-10-25',
      '2026-10-26',
      '2026-10-27',
    ];
    stubApi({
      'GET /environments/3': { body: { ...base, docker } },
      [`GET /environments/3/activity?${new URLSearchParams({ days: '7', tz })}`]: {
        body: {
          days: days.map((date, i) => ({
            date,
            counts: Array.from({ length: 24 }, (_, h) => (h === 13 ? i + 1 : 0)),
          })),
        },
      },
    });
    const wrapper = await mountTile(base);
    const heatmap = wrapper.find('.heatmap');
    expect(heatmap.findAll('.days li').map((li) => li.text())).toEqual([
      'Wed',
      'Thu',
      'Fri',
      'Sat',
      'Sun',
      'Mon',
      'Tue',
    ]);
    const cells = heatmap.findAll('rect');
    expect(cells).toHaveLength(7 * 24);
    expect(cells[6 * 24 + 13]?.find('title').text()).toBe('Tue 13:00, 7 events');
  });

  it('leaves the heatmap out until the counts arrive', async () => {
    stubApi({ 'GET /environments/3': { body: { ...base, docker } } });
    const wrapper = await mountTile(base);
    expect(wrapper.find('.heatmap').exists()).toBe(false);
  });

  it('does not ask other users for the disk', async () => {
    const fetch = stubApi({ 'GET /environments/3': { body: { ...base, docker } } });
    const other = await mountTile(base);
    expect(fetch.mock.calls.map(([url]) => url)).not.toContain('/api/v1/environments/3/disk');
    expect(other.find('.big').exists()).toBe(false);
    expect(gaugeLabels(other)[2]).toBe('Disk unknown');
  });

  it('opens the environment from anywhere on the tile but the settings', async () => {
    currentUser.value = admin;
    stubApi({ 'GET /environments/3': { body: { ...base, docker } } });
    const wrapper = await mountTile(base);
    const push = vi.spyOn(wrapper.router, 'push');
    await wrapper.find('.gear').trigger('click');
    expect(push).not.toHaveBeenCalled();
    // A click in the settings dialog stays there.
    await wrapper.find('dialog .facts').trigger('click');
    expect(push).not.toHaveBeenCalled();
    await wrapper.find('.stats').trigger('click');
    expect(push).toHaveBeenCalledWith({ name: 'env', params: { env: '3' } });
  });

  it('opens the settings from the gear and passes on changes', async () => {
    currentUser.value = admin;
    const env = { ...base, online: false, agent: { enrolled: true, version: '0.9.0' } };
    stubApi({ 'DELETE /environments/3/credentials': { status: 204 } });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = await mountTile(env);
    expect(wrapper.find('dialog').exists()).toBe(false);
    await wrapper.find('.gear').trigger('click');
    expect(wrapper.find('dialog').attributes('aria-label')).toBe('Environment prod');
    const revoke = wrapper.findAll('dialog button').find((b) => b.text() === 'Revoke agent');
    await revoke?.trigger('click');
    await flushPromises();
    expect(wrapper.emitted('changed')).toHaveLength(1);
    await wrapper.find('dialog .close').trigger('click');
    expect(wrapper.find('dialog').exists()).toBe(false);
  });

  it('has no settings for other users', async () => {
    currentUser.value = { ...admin, admin: false };
    stubApi({});
    const wrapper = await mountTile({ ...base, online: false });
    expect(wrapper.find('.gear').exists()).toBe(false);
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

  // The live stream takes over from the stored summary with every sample.
  it('updates the gauges from the live usage stream', async () => {
    stubApi({
      'GET /environments/3': { body: { ...base, docker } },
      'GET /environments/3/stats/summary': {
        body: { sampled_at: '', cpu_percent: 200, memory_usage: 256 * 1024 ** 2, containers: 3 },
      },
    });
    const wrapper = await mountTile(base);
    expect(gaugeLabels(wrapper).slice(0, 2)).toEqual(['CPU 50%', 'Memory 25%']);
    const ws = sockets.find((s) => s.url.endsWith('/environments/3/metrics/live'))!;
    ws.message({
      time: '2026-10-27T12:00:05Z',
      containers: [],
      skipped: 0,
      total: {
        cpu_percent: 100,
        memory_usage: 512 * 1024 ** 2,
        network_rx: 0,
        network_tx: 0,
        block_read: 0,
        block_write: 0,
      },
    });
    await flushPromises();
    expect(gaugeLabels(wrapper).slice(0, 2)).toEqual(['CPU 25%', 'Memory 50%']);
    wrapper.unmount();
    expect(ws.closed).toBe(true);
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
    expect(sockets).toHaveLength(0);
  });
});
