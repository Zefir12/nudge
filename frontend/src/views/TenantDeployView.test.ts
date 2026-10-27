import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { Allowance, TenantSpec } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { stubApi, user, type Routes } from '../test-utils';
import TenantDeployView from './TenantDeployView.vue';

const MiB = 1024 * 1024;

const allowance = (images: string[] = []): Allowance => ({
  user: 2,
  limits: {
    cpus_milli: 2000,
    memory_bytes: 2048 * MiB,
    disk_bytes: 10 * 1024 * MiB,
    max_containers: 5,
    port_min: 8000,
    port_max: 8009,
    images,
  },
  used: { cpus_milli: 500, memory_bytes: 512 * MiB, disk_bytes: 0, containers: 1 },
  left: { cpus_milli: 1500, memory_bytes: 1536 * MiB, disk_bytes: 10 * 1024 * MiB, containers: 4 },
});

const volumes = [{ name: 'u2-data', driver: 'local', labels: {} }];
const networks = [
  { id: 'n1', name: 'nudge-u-2', driver: 'bridge', labels: {} },
  { id: 'n2', name: 'u2-front', driver: 'bridge', labels: {} },
];

const spec: TenantSpec = {
  image: 'nginx:1.29',
  name: 'web',
  command: [],
  entrypoint: [],
  env: [],
  cpus_milli: 500,
  memory_bytes: 256 * MiB,
  ports: [],
  mounts: [],
  networks: [],
  restart_policy: 'unless-stopped',
  healthcheck: null,
  labels: {},
};

function stub(routes: Routes, a = allowance()) {
  return stubApi({
    'GET /environments/1/allowance': { body: a },
    'GET /environments/1/volumes': { body: volumes },
    'GET /environments/1/networks': { body: networks },
    ...routes,
  });
}

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(TenantDeployView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

const body = (fetch: ReturnType<typeof stubApi>, key: string) => {
  const call = fetch.mock.calls.find(([url, init]) => `${init?.method ?? 'GET'} ${url}`.endsWith(key));
  return JSON.parse(String(call?.[1]?.body));
};

beforeEach(() => {
  currentUser.value = user;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('TenantDeployView', () => {
  it('caps the sliders and host ports at the allowance', async () => {
    stub({});
    const { wrapper } = await mountAt('/env/1/deploy');
    expect(wrapper.find('h1').text()).toBe('Deploy a container');
    const cpus = wrapper.find('input[name=cpus_milli]');
    expect(cpus.attributes('max')).toBe('1500');
    expect((cpus.element as HTMLInputElement).value).toBe('500');
    expect(wrapper.find('input[name=memory_bytes]').attributes('max')).toBe('1536');
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Add port')!
      .trigger('click');
    const port = wrapper.find('input[name="ports.0.host_port"]');
    expect(port.attributes('min')).toBe('8000');
    expect(port.attributes('max')).toBe('8009');
    // Free text when the allowance allows any image.
    expect(wrapper.find('input[name=image]').exists()).toBe(true);
  });

  it('offers the allowed images as a select', async () => {
    stub({}, allowance(['nginx:1.29', 'redis:7']));
    const { wrapper } = await mountAt('/env/1/deploy');
    const options = wrapper.findAll('select[name=image] option').map((o) => o.text());
    expect(options).toEqual(['Choose an image', 'nginx:1.29', 'redis:7']);
  });

  it('says so without an allowance', async () => {
    stub({}, { user: 2, limits: null, used: allowance().used, left: null });
    const { wrapper } = await mountAt('/env/1/deploy');
    expect(wrapper.find('form').exists()).toBe(false);
    expect(wrapper.text()).toContain('You have no allowance in this environment');
  });

  it('creates a new volume, deploys and opens the container', async () => {
    const fetch = stub({
      'POST /environments/1/volumes': { status: 201, body: { name: 'u2-cache', driver: 'local' } },
      'POST /environments/1/user-containers': { status: 201, body: { id: 'c1', warnings: [] } },
      'GET /environments/1/containers/c1': { status: 404, body: { error: 'container not found' } },
    });
    const { wrapper, router } = await mountAt('/env/1/deploy');
    await wrapper.find('input[name=image]').setValue('nginx:1.29');
    await wrapper.find('input[name=name]').setValue('web');
    await wrapper.find('input[name=cpus_milli]').setValue('1000');
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Add volume')!
      .trigger('click');
    await wrapper.find('select[name="mounts.0.volume"]').setValue('');
    await wrapper.find('input[name="mounts.0.new"]').setValue('cache');
    await wrapper.find('input[name="mounts.0.target"]').setValue('/cache');
    await wrapper.find('input[name=networks][value=u2-front]').setValue(true);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(body(fetch, 'POST /api/v1/environments/1/volumes')).toEqual({ name: 'cache' });
    expect(body(fetch, 'POST /api/v1/environments/1/user-containers')).toEqual({
      ...spec,
      cpus_milli: 1000,
      mounts: [{ volume: 'u2-cache', target: '/cache', read_only: false }],
      networks: ['u2-front'],
      start: true,
    });
    expect(router.currentRoute.value.fullPath).toBe('/env/1/containers/c1');
  });

  it('reuses a volume it created when the deploy is retried', async () => {
    let attempts = 0;
    const fetch = stub({
      'POST /environments/1/volumes': { status: 201, body: { name: 'u2-cache', driver: 'local' } },
      'POST /environments/1/user-containers': () =>
        ++attempts === 1
          ? { status: 409, body: { error: 'over your allowance: memory' } }
          : { status: 201, body: { id: 'c1', warnings: [] } },
      'GET /environments/1/containers/c1': { status: 404, body: { error: 'container not found' } },
    });
    const { wrapper } = await mountAt('/env/1/deploy');
    await wrapper.find('input[name=image]').setValue('nginx:1.29');
    await wrapper.find('input[name=name]').setValue('web');
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Add volume')!
      .trigger('click');
    await wrapper.find('select[name="mounts.0.volume"]').setValue('');
    await wrapper.find('input[name="mounts.0.new"]').setValue('cache');
    await wrapper.find('input[name="mounts.0.target"]').setValue('/cache');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('over your allowance: memory');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    const volumeCalls = fetch.mock.calls.filter(
      ([url, init]) => init?.method === 'POST' && String(url).endsWith('/volumes'),
    );
    expect(volumeCalls).toHaveLength(1);
    expect(attempts).toBe(2);
  });

  it('shows the API field errors next to their fields', async () => {
    stub({
      'POST /environments/1/user-containers': {
        status: 400,
        body: {
          error: 'invalid container settings',
          fields: { image: 'not an image reference', 'ports.0.host_port': 'outside your port range' },
        },
      },
    });
    const { wrapper } = await mountAt('/env/1/deploy');
    await wrapper.find('input[name=image]').setValue('Bad Image');
    await wrapper.find('input[name=name]').setValue('web');
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Add port')!
      .trigger('click');
    await wrapper.find('input[name="ports.0.container_port"]').setValue('80');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('invalid container settings');
    expect(wrapper.text()).toContain('not an image reference');
    expect(wrapper.text()).toContain('host port: outside your port range');
  });

  it('edits a container: its own limits count as left and saving recreates it', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const health = {
      command: ['/bin/check'],
      interval_seconds: 30,
      timeout_seconds: 5,
      start_period_seconds: 0,
      retries: 3,
    };
    const fetch = stub({
      'GET /environments/1/user-containers/c1/spec': { body: { ...spec, healthcheck: health } },
      'POST /environments/1/user-containers/c1/recreate': { body: { id: 'c2', warnings: [] } },
      'GET /environments/1/containers/c2': { status: 404, body: { error: 'container not found' } },
    });
    const { wrapper, router } = await mountAt('/env/1/containers/c1/redeploy');
    expect(wrapper.find('h1').text()).toBe('Edit web');
    expect(wrapper.find('input[name=cpus_milli]').attributes('max')).toBe('2000');
    expect(wrapper.find('input[name=memory_bytes]').attributes('max')).toBe('1792');
    expect((wrapper.find('textarea[name="healthcheck.command"]').element as HTMLTextAreaElement).value).toBe(
      '/bin/check',
    );
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(body(fetch, '/user-containers/c1/recreate')).toEqual({ ...spec, healthcheck: health });
    expect(router.currentRoute.value.fullPath).toBe('/env/1/containers/c2');
  });
});
