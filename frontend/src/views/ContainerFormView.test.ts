import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { ContainerSpec } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import ContainerFormView from './ContainerFormView.vue';

const spec: ContainerSpec = {
  image: 'nginx:1.29',
  name: 'web',
  command: [],
  env: [],
  ports: [],
  mounts: [],
  networks: [],
  restart_policy: 'no',
  labels: {},
};

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(ContainerFormView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

const body = (fetch: ReturnType<typeof stubApi>, suffix: string) =>
  JSON.parse(String(fetch.mock.calls.find(([url]) => String(url).endsWith(suffix))?.[1]?.body));

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ContainerFormView', () => {
  it('creates and starts a container, then opens it', async () => {
    const fetch = stubApi({
      'POST /environments/1/containers': { status: 201, body: { id: 'c1', warnings: [] } },
      'GET /environments/1/containers/c1': { status: 404, body: { error: 'container not found' } },
    });
    const { wrapper, router } = await mountAt('/env/1/containers/new');
    expect(wrapper.find('h1').text()).toBe('New container');
    await wrapper.find('input[name=image]').setValue('nginx:1.29');
    await wrapper.find('input[name=name]').setValue('web');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(body(fetch, '/containers')).toEqual({ ...spec, start: true });
    expect(router.currentRoute.value.fullPath).toBe('/env/1/containers/c1');
  });

  it('shows field errors from the API', async () => {
    stubApi({
      'POST /environments/1/containers': {
        status: 400,
        body: { error: 'invalid container settings', fields: { name: '2 or more letters' } },
      },
    });
    const { wrapper, router } = await mountAt('/env/1/containers/new');
    await wrapper.find('input[name=image]').setValue('nginx');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('invalid container settings');
    expect(wrapper.find('label .error').text()).toBe('2 or more letters');
    expect(router.currentRoute.value.name).toBe('container-new');
  });

  it('edits a container by recreating it with the changed settings', async () => {
    const fetch = stubApi({
      'GET /environments/1/containers/abc/spec': { body: spec },
      'POST /environments/1/containers/abc/recreate': { body: { id: 'new1', warnings: [] } },
      'GET /environments/1/containers/new1': { status: 404, body: { error: 'container not found' } },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper, router } = await mountAt('/env/1/containers/abc/edit');
    expect(wrapper.find('h1').text()).toBe('Edit web');
    expect((wrapper.find('input[name=image]').element as HTMLInputElement).value).toBe('nginx:1.29');
    await wrapper.find('input[name=image]').setValue('nginx:1.30');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(body(fetch, '/recreate')).toEqual({ pull: false, spec: { ...spec, image: 'nginx:1.30' } });
    expect(router.currentRoute.value.fullPath).toBe('/env/1/containers/new1');
  });
});
