import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { ContainerDetail } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import ContainerView from './ContainerView.vue';

const detail: ContainerDetail = {
  id: 'abc123def4567890',
  name: 'web',
  image: 'nginx:1.29',
  image_id: 'sha256:0123456789abcdef',
  created_at: '2026-10-01T12:00:00Z',
  state: {
    status: 'exited',
    running: false,
    paused: false,
    restarting: false,
    oom_killed: true,
    dead: false,
    pid: 0,
    exit_code: 137,
    error: '',
    started_at: '2026-10-01T12:00:01Z',
    finished_at: '2026-10-02T08:00:00Z',
    health: '',
  },
  restart_count: 2,
  path: 'nginx',
  args: ['-g', 'daemon off;'],
  config: {
    hostname: 'abc123',
    user: '',
    env: ['PATH=/usr/bin', 'MODE=prod'],
    cmd: [],
    entrypoint: [],
    working_dir: '/srv',
    exposed_ports: ['80/tcp'],
    labels: { 'com.example.b': '2', 'com.example.a': '1' },
    tty: false,
  },
  restart_policy: { name: 'unless-stopped', maximum_retry_count: 0 },
  network_mode: 'bridge',
  privileged: false,
  ports: [{ private_port: 80, protocol: 'tcp', host_ip: '', public_port: 8080 }],
  mounts: [
    {
      type: 'volume',
      name: 'data',
      source: '/var/lib/docker/volumes/data/_data',
      destination: '/data',
      rw: true,
    },
    { type: 'bind', name: '', source: '/etc/app', destination: '/config', rw: false },
  ],
  networks: [
    {
      name: 'front',
      network_id: 'n1',
      ip_address: '172.18.0.2',
      gateway: '172.18.0.1',
      mac_address: '',
      aliases: ['web'],
    },
  ],
};

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(ContainerView, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ContainerView', () => {
  it('shows the container', async () => {
    stubApi({ 'GET /environments/1/containers/web': { body: detail } });
    const wrapper = await mountAt('/env/1/containers/web');
    const text = wrapper.text();
    expect(wrapper.find('h1').text()).toContain('web');
    expect(wrapper.find('h1').text()).toContain('exited');
    for (const want of [
      'abc123def456', // short ID
      'nginx -g daemon off;',
      '137',
      'unless-stopped',
      '8080 → 80/tcp',
      '/config',
      '/etc/app',
      '172.18.0.2',
      'MODE=prod',
    ]) {
      expect(text).toContain(want);
    }
    expect(text.indexOf('com.example.a')).toBeLessThan(text.indexOf('com.example.b'));
    const links = wrapper.findAll('a').map((a) => a.attributes('href'));
    expect(links).toContain('/env/1/containers');
    expect(links).toContain('/env/1/volumes/data');
    expect(links).toContain('/env/1/networks/n1');
    expect(links).toContain('/env/1/images/sha256:0123456789abcdef');
  });

  it('shows a missing container', async () => {
    stubApi({
      'GET /environments/1/containers/gone': { status: 404, body: { error: 'container not found' } },
    });
    const wrapper = await mountAt('/env/1/containers/gone');
    expect(wrapper.find('[role=alert]').text()).toContain('container not found');
    expect(wrapper.find('h1').exists()).toBe(false);
  });

  it('escapes the ID in the API path', async () => {
    const fetch = stubApi({ 'GET /environments/1/containers/a%20b': { body: detail } });
    await mountAt('/env/1/containers/a%20b');
    expect(fetch).toHaveBeenCalledWith('/api/v1/environments/1/containers/a%20b', expect.anything());
  });
});
