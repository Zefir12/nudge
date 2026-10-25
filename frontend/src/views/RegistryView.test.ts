import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import RegistryView from './RegistryView.vue';

const forge = {
  id: 1,
  name: 'Forge',
  address: 'git.example.com',
  username: 'bot',
  kind: 'forgejo',
  owners: ['zefir'],
  plain_http: false,
  created_at: '2026-10-24T12:00:00Z',
  updated_at: '2026-10-24T12:00:00Z',
};

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(RegistryView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

const names = (wrapper: ReturnType<typeof mount>) => wrapper.findAll('.repos li').map((li) => li.text());

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  currentUser.value = null;
});

describe('RegistryView', () => {
  it('lists repositories page by page', async () => {
    stubApi({
      'GET /registries/1': { body: forge },
      'GET /registries/1/repositories?q=&cursor=': {
        body: { repositories: [{ name: 'zefir/app' }], next: 'zefir/app' },
      },
      'GET /registries/1/repositories?q=&cursor=zefir%2Fapp': {
        body: { repositories: [{ name: 'zefir/db' }] },
      },
    });
    const { wrapper } = await mountAt('/registries/1');
    expect(wrapper.find('h1').text()).toBe('Forge');
    expect(names(wrapper)).toEqual(['zefir/app']);
    expect(wrapper.find('.repos a').attributes('href')).toBe('/registries/1/repository?name=zefir/app');
    const more = wrapper.findAll('button').find((b) => b.text() === 'Load more')!;
    await more.trigger('click');
    await flushPromises();
    expect(names(wrapper)).toEqual(['zefir/app', 'zefir/db']);
    expect(wrapper.findAll('button').some((b) => b.text() === 'Load more')).toBe(false);
  });

  it('searches', async () => {
    stubApi({
      'GET /registries/1': { body: forge },
      'GET /registries/1/repositories?q=&cursor=': { body: { repositories: [{ name: 'zefir/app' }] } },
      'GET /registries/1/repositories?q=db&cursor=': { body: { repositories: [] } },
    });
    const { wrapper } = await mountAt('/registries/1');
    await wrapper.find('input[name=q]').setValue(' db ');
    await wrapper.findAll('form')[1]!.trigger('submit');
    await flushPromises();
    expect(names(wrapper)).toEqual([]);
    expect(wrapper.text()).toContain('No repositories.');
  });

  it('opens a repository by name when the registry has no catalog', async () => {
    stubApi({
      'GET /registries/1': { body: { ...forge, kind: 'generic', address: 'docker.io' } },
      'GET /registries/1/repositories?q=&cursor=': {
        status: 409,
        body: { error: "registry doesn't list repositories; open one by name" },
      },
    });
    const { wrapper, router } = await mountAt('/registries/1');
    expect(wrapper.text()).toContain("doesn't list its repositories");
    expect(wrapper.find('[role=alert]').exists()).toBe(false);
    await wrapper.find('input[name=open]').setValue(' library/nginx ');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('registry-repository');
    expect(router.currentRoute.value.query).toEqual({ name: 'library/nginx' });
  });

  it('shows a listing error', async () => {
    stubApi({
      'GET /registries/1': { body: forge },
      'GET /registries/1/repositories?q=&cursor=': { status: 502, body: { error: 'registry unreachable' } },
    });
    const { wrapper } = await mountAt('/registries/1');
    expect(wrapper.find('[role=alert]').text()).toBe('registry unreachable');
  });
});
