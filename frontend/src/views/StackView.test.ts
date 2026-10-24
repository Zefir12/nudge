import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, stubSockets, type FakeWebSocket } from '../test-utils';
import StackView from './StackView.vue';

let sockets: FakeWebSocket[];

beforeEach(() => {
  currentUser.value = admin;
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const ctr = (id: string, name: string, state: string) => ({
  id,
  name,
  image: 'nginx:alpine',
  image_id: '',
  command: '',
  created_at: null,
  state,
  status: state === 'running' ? 'Up 2 minutes' : 'Exited (0)',
  health: '',
  ports: [],
  labels: {},
  networks: [],
  mounts: [],
});

const compose = 'services:\n  app:\n    image: nginx\n  worker:\n    image: busybox\n';
const web = () => ({
  name: 'web',
  containers: 2,
  running: 1,
  compose,
  env: 'TAG=1\n',
  services: [
    { name: 'app', containers: [ctr('a1', 'web-app-1', 'running'), ctr('a2', 'web-app-2', 'exited')] },
    { name: 'old', containers: [] },
  ],
});

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/env/1/stacks/web');
  const wrapper = mount(StackView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

const button = (wrapper: ReturnType<typeof mount>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text)!;

describe('StackView', () => {
  it('shows the services with their containers and follows their logs', async () => {
    stubApi({ 'GET /environments/1/stacks/web': { body: web() } });
    const { wrapper } = await mountView();
    const rows = wrapper.findAll('tbody tr').map((r) =>
      r
        .findAll('td')
        .slice(0, 3)
        .map((c) => c.text()),
    );
    expect(rows).toEqual([
      ['app', 'web-app-1', 'running'],
      ['', 'web-app-2', 'exited'],
      ['worker', 'not created'],
      ['old', 'not created'],
    ]);
    expect(wrapper.find('tbody a').attributes('href')).toBe('/env/1/containers/a1');
    const logs = sockets.filter((s) => s.url.includes('/logs'));
    expect(logs.map((s) => new URL(s.url).pathname)).toEqual([
      '/api/v1/environments/1/containers/a1/logs',
      '/api/v1/environments/1/containers/a2/logs',
    ]);
    // Down is possible, deleting isn't while there are containers.
    expect(button(wrapper, 'Delete stack')).toBeUndefined();
  });

  it('updates, shows compose output and the update history', async () => {
    let updated = false;
    stubApi({
      'GET /environments/1/stacks/web': { body: web() },
      'GET /environments/1/updates/history?kind=stack&target=web': () => ({
        body: updated
          ? [
              {
                id: 1,
                kind: 'stack',
                target: 'web',
                service: 'app',
                image: 'ghcr.io/o/app:v1',
                old_image_id: 'sha256:aaaaaaaaaaaaaaaa',
                old_digest: 'sha256:old',
                new_image_id: 'sha256:bbbbbbbbbbbbbbbb',
                new_digest: 'sha256:new',
                user: 'admin',
                time: '2026-10-24T12:00:00Z',
              },
            ]
          : [],
      }),
      'POST /environments/1/stacks/web/update': () => {
        updated = true;
        return {
          raw: '{"stream":"stderr","line":" Container web-app-1  Started"}\n{"exit_code":0}\n',
        };
      },
    });
    const { wrapper } = await mountView();
    expect(wrapper.text()).not.toContain('Updates');
    await button(wrapper, 'Update').trigger('click');
    await flushPromises();
    expect(wrapper.find('.output').text()).toBe('Container web-app-1  Started');
    expect(wrapper.text()).toContain('Update finished.');
    expect(wrapper.text()).toContain('Updates');
    expect(wrapper.text()).toContain('aaaaaaaaaaaa (sha256:old)');
    expect(wrapper.text()).toContain('bbbbbbbbbbbb (sha256:new)');
  });

  it('deploys without pulling', async () => {
    let body: unknown;
    stubApi({
      'GET /environments/1/stacks/web': { body: web() },
      'POST /environments/1/stacks/web/up': (init) => {
        body = JSON.parse(init!.body as string);
        return { raw: '{"exit_code":0}\n' };
      },
    });
    const { wrapper } = await mountView();
    await button(wrapper, 'Deploy').trigger('click');
    await flushPromises();
    expect(body).toEqual({ pull: false });
    expect(wrapper.text()).toContain('Deploy finished.');
  });

  it('reports a failed deploy', async () => {
    stubApi({
      'GET /environments/1/stacks/web': { body: web() },
      'POST /environments/1/stacks/web/up': {
        raw: '{"stream":"stderr","line":"pull access denied"}\n{"exit_code":1}\n',
      },
    });
    const { wrapper } = await mountView();
    await button(wrapper, 'Deploy').trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('Deploy failed: compose exited with code 1.');
  });

  it('keeps the page and its log streams when a refresh fails', async () => {
    let gets = 0;
    stubApi({
      'GET /environments/1/stacks/web': () =>
        ++gets === 1 ? { body: web() } : { status: 502, body: { error: 'environment unreachable' } },
      'POST /environments/1/stacks/web/up': { raw: '{"exit_code":0}\n' },
    });
    const { wrapper } = await mountView();
    await button(wrapper, 'Deploy').trigger('click');
    await flushPromises();
    expect(gets).toBe(2);
    expect(wrapper.text()).toContain("Couldn't refresh the stack: environment unreachable");
    expect(wrapper.findAll('tbody tr')).toHaveLength(4);
    expect(sockets.filter((s) => s.url.includes('/logs')).every((s) => !s.closed)).toBe(true);
  });

  it('takes the stack down after a confirm', async () => {
    let body: unknown;
    stubApi({
      'GET /environments/1/stacks/web': { body: web() },
      'POST /environments/1/stacks/web/down': (init) => {
        body = JSON.parse(init!.body as string);
        return { raw: '{"exit_code":0}\n' };
      },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const { wrapper } = await mountView();
    await button(wrapper, 'Down').trigger('click');
    expect(body).toBeUndefined();
    confirm.mockReturnValue(true);
    await wrapper.find('.actions input[type="checkbox"]').setValue(true);
    await button(wrapper, 'Down').trigger('click');
    await flushPromises();
    expect(confirm.mock.calls[1]![0]).toContain('volumes');
    expect(body).toEqual({ volumes: true });
  });

  it('saves edited files', async () => {
    let saved: unknown;
    stubApi({
      'GET /environments/1/stacks/web': { body: web() },
      'POST /stacks/check': { body: { errors: [] } },
      'PUT /environments/1/stacks/web': (init) => {
        saved = JSON.parse(init!.body as string);
        return { status: 204 };
      },
    });
    const { wrapper } = await mountView();
    const [composeArea] = wrapper.findAll('textarea');
    expect((composeArea!.element as HTMLTextAreaElement).value).toBe(compose);
    expect(button(wrapper, 'Save').attributes('disabled')).toBeDefined();

    await composeArea!.setValue('services:\n  app:\n    image: nginx:1\n');
    expect(wrapper.text()).toContain('Unsaved changes.');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(saved).toEqual({ compose: 'services:\n  app:\n    image: nginx:1\n', env: 'TAG=1\n' });
    expect(wrapper.text()).toContain('Saved.');
  });

  it('deletes a stack that is down', async () => {
    const down = { ...web(), containers: 0, running: 0, services: [] };
    const fetch = stubApi({
      'GET /environments/1/stacks/web': { body: down },
      'DELETE /environments/1/stacks/web': { status: 204 },
      'GET /environments/1/stacks': { body: [] },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper, router } = await mountView();
    expect(button(wrapper, 'Down').attributes('disabled')).toBeDefined();
    await button(wrapper, 'Delete stack').trigger('click');
    await flushPromises();
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(true);
    expect(router.currentRoute.value.fullPath).toBe('/env/1/stacks');
  });
});
