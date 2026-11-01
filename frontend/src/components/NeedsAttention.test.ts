import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { ContainerSummary, ImageSummary, ImageUpdate } from '../api/client';
import { createAppRouter } from '../router';
import { stubApi, stubSockets, type FakeWebSocket } from '../test-utils';
import NeedsAttention from './NeedsAttention.vue';

const container = (name: string, state: ContainerSummary['state'], image = 'nginx:1'): ContainerSummary => ({
  id: `${name}-id`,
  name,
  image,
  image_id: `sha256:${image}`,
  command: '',
  created_at: null,
  state,
  status: '',
  health: '',
  ports: [],
  labels: {},
  networks: [],
  mounts: [],
});

const image = (id: string, dangling: boolean, size = 1e6): ImageSummary => ({
  id,
  tags: dangling ? [] : [`${id}:latest`],
  digests: [],
  created_at: null,
  size,
  labels: {},
  dangling,
});

const update = (img: string, status: ImageUpdate['status']): ImageUpdate => ({
  image: img,
  image_id: `sha256:${img}`,
  status,
  local_digest: '',
  remote_digest: '',
  error: '',
  checked_at: '2026-10-24T12:00:00Z',
});

const mountPanel = () =>
  mount(NeedsAttention, {
    props: { env: '1' },
    global: { plugins: [createAppRouter(createMemoryHistory())] },
  });

let sockets: FakeWebSocket[];

beforeEach(() => {
  sockets = stubSockets();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('NeedsAttention', () => {
  it('lists updates, stopped containers and untagged images', async () => {
    const stopped = ['a', 'b', 'c', 'd', 'e', 'f'].map((n) => container(n, 'exited'));
    stubApi({
      'GET /environments/1/containers': {
        body: [
          container('web', 'running', 'web:1'),
          container('db', 'running'),
          container('old', 'dead'),
          ...stopped,
        ],
      },
      'GET /environments/1/images': {
        body: [image('x', true, 2e6), image('y', true, 3e6), image('z', false)],
      },
      'GET /environments/1/updates': { body: [update('web:1', 'available'), update('nginx:1', 'current')] },
    });
    const wrapper = mountPanel();
    await flushPromises();

    const updates = wrapper.find('.updates');
    expect(updates.text()).toContain('1 update available');
    expect(updates.find('.names a').attributes('href')).toBe('/env/1/containers/web-id');

    const down = wrapper.find('.stopped');
    expect(down.text()).toContain('7 stopped containers');
    expect(down.findAll('.names a').map((a) => a.text())).toEqual(['old', 'a', 'b', 'c', 'd']);
    expect(down.text()).toContain('and 2 more');

    const dangling = wrapper.find('.dangling');
    expect(dangling.text()).toContain('2 untagged images');
    expect(dangling.text()).toContain('5.0 MB');
    expect(dangling.find('a').attributes('href')).toBe('/env/1/images');
  });

  it('says when nothing needs attention, and skips a failed update list', async () => {
    stubApi({
      'GET /environments/1/containers': { body: [container('web', 'running')] },
      'GET /environments/1/images': { body: [image('z', false)] },
      'GET /environments/1/updates': { status: 500, body: { error: 'internal error' } },
    });
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.text()).toContain('Nothing needs attention.');
    expect(wrapper.find('[role=alert]').exists()).toBe(false);
  });

  it('reloads on container events', async () => {
    vi.useFakeTimers();
    const fetch = stubApi({
      'GET /environments/1/containers': { body: [] },
      'GET /environments/1/images': { body: [] },
      'GET /environments/1/updates': { body: [] },
    });
    mountPanel();
    await flushPromises();
    const calls = () => fetch.mock.calls.filter(([url]) => url.endsWith('/containers')).length;
    expect(calls()).toBe(1);
    expect(sockets[0]!.url).toContain('type=container%2Cimage');
    sockets[0]!.message({ time: '2026-10-24T12:00:00Z', type: 'container', action: 'die', id: 'x' });
    await vi.advanceTimersByTimeAsync(1000);
    expect(calls()).toBe(2);
  });

  it('shows errors', async () => {
    stubApi({
      'GET /environments/1/containers': { status: 503, body: { error: 'the environment is offline' } },
      'GET /environments/1/images': { body: [] },
      'GET /environments/1/updates': { body: [] },
    });
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('the environment is offline');
  });
});
