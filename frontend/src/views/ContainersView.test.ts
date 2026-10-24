import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { ContainerSummary } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, stubSockets, type FakeWebSocket } from '../test-utils';
import ContainersView from './ContainersView.vue';

const base: ContainerSummary = {
  id: 'abc123',
  name: 'web',
  image: 'nginx:1.29',
  image_id: 'sha256:img1',
  command: 'nginx',
  created_at: '2026-10-01T12:00:00Z',
  state: 'running',
  status: 'Up 2 hours',
  health: 'healthy',
  ports: [{ private_port: 80, protocol: 'tcp', host_ip: '0.0.0.0', public_port: 8080 }],
  labels: {},
  networks: ['front'],
  mounts: [],
};
const containers: ContainerSummary[] = [
  base,
  {
    ...base,
    id: 'def456',
    name: 'db',
    image: 'postgres:18',
    state: 'exited',
    status: 'Exited (0)',
    health: '',
    ports: [],
  },
];

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(ContainersView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

let sockets: FakeWebSocket[];

beforeEach(() => {
  currentUser.value = admin;
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ContainersView', () => {
  it('lists the containers with links to their details', async () => {
    stubApi({ 'GET /environments/1/containers': { body: containers } });
    const { wrapper } = await mountAt('/env/1/containers');
    const rows = wrapper.findAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('web');
    expect(rows[0]!.text()).toContain('running · healthy');
    expect(rows[0]!.text()).toContain('0.0.0.0:8080 → 80/tcp');
    expect(rows[0]!.find('a').attributes('href')).toBe('/env/1/containers/abc123');
    expect(rows[1]!.text()).toContain('exited');
  });

  it.each([
    { name: 'search by name', query: 'db', state: 'all', want: ['db'] },
    { name: 'search by image, ignoring case', query: 'NGINX', state: 'all', want: ['web'] },
    { name: 'running only', query: '', state: 'running', want: ['web'] },
    { name: 'stopped only', query: '', state: 'stopped', want: ['db'] },
    { name: 'no match', query: 'redis', state: 'all', want: [] },
  ])('filters: $name', async ({ query, state, want }) => {
    stubApi({ 'GET /environments/1/containers': { body: containers } });
    const { wrapper } = await mountAt('/env/1/containers');
    await wrapper.find('input[type=search]').setValue(query);
    await wrapper.find('select').setValue(state);
    const names = wrapper.findAll('tbody tr td:first-child').map((td) => td.text());
    expect(names).toEqual(want);
    if (want.length === 0) expect(wrapper.text()).toContain('No container matches.');
  });

  it('reloads once after a burst of container events', async () => {
    let calls = 0;
    stubApi({
      'GET /environments/1/containers': () => {
        calls++;
        return { body: calls === 1 ? containers : [containers[0]] };
      },
    });
    const { wrapper } = await mountAt('/env/1/containers');
    expect(sockets).toHaveLength(1);
    expect(sockets[0]!.url).toMatch(/\/api\/v1\/environments\/1\/events\?type=container$/);
    const event = { time: '2026-10-24T12:00:00Z', type: 'container', id: 'def456' };
    sockets[0]!.message({ ...event, action: 'exec_start' });
    sockets[0]!.message({ ...event, action: 'die' });
    sockets[0]!.message({ ...event, action: 'destroy' });
    await new Promise((r) => setTimeout(r, 350));
    await flushPromises();
    expect(calls).toBe(2);
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
    wrapper.unmount();
    expect(sockets[0]!.closed).toBe(true);
  });

  it('says when there are no containers', async () => {
    stubApi({ 'GET /environments/1/containers': { body: [] } });
    const { wrapper } = await mountAt('/env/1/containers');
    expect(wrapper.text()).toContain('No containers.');
    expect(wrapper.find('table').exists()).toBe(false);
  });

  it('shows loading until the list arrives', async () => {
    let answer: (r: Response) => void = () => {};
    vi.stubGlobal(
      'fetch',
      vi.fn(() => new Promise<Response>((resolve) => (answer = resolve))),
    );
    const { wrapper } = await mountAt('/env/1/containers');
    expect(wrapper.text()).toContain('Loading…');
    answer(new Response(JSON.stringify(containers)));
    await flushPromises();
    expect(wrapper.findAll('tbody tr')).toHaveLength(2);
  });

  it('shows an offline environment and retries', async () => {
    let offline = true;
    stubApi({
      'GET /environments/2/containers': () =>
        offline ? { status: 503, body: { error: 'the environment is offline' } } : { body: containers },
    });
    const { wrapper } = await mountAt('/env/2/containers');
    expect(wrapper.find('[role=alert]').text()).toContain('the environment is offline');
    offline = false;
    await wrapper.find('[role=alert] button').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=alert]').exists()).toBe(false);
    expect(wrapper.findAll('tbody tr')).toHaveLength(2);
  });

  it('reloads the list after an action', async () => {
    let state = 'running';
    stubApi({
      'GET /environments/1/containers': () => ({ body: [{ ...base, state }] }),
      'POST /environments/1/containers/abc123/stop': () => {
        state = 'exited';
        return { status: 204 };
      },
    });
    const { wrapper } = await mountAt('/env/1/containers');
    const stop = wrapper.findAll('tbody button').find((b) => b.text() === 'Stop');
    await stop!.trigger('click');
    await flushPromises();
    expect(wrapper.find('tbody tr').text()).toContain('exited');
    expect(wrapper.findAll('tbody button').map((b) => b.text())).toEqual(['Start', 'Remove']);
  });

  it('reloads when the environment changes', async () => {
    stubApi({
      'GET /environments/1/containers': { body: containers },
      'GET /environments/2/containers': { body: [base] },
    });
    const { wrapper, router } = await mountAt('/env/1/containers');
    expect(wrapper.findAll('tbody tr')).toHaveLength(2);
    await router.push('/env/2/containers');
    await flushPromises();
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
  });
});
