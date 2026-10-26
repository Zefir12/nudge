import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { stubApi } from '../test-utils';
import RegistriesView from './RegistriesView.vue';

const mountView = () =>
  mount(RegistriesView, { global: { plugins: [createAppRouter(createMemoryHistory())] } });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const ghcr = {
  id: 1,
  name: 'GitHub',
  address: 'ghcr.io',
  username: 'bot',
  kind: 'generic',
  owners: [],
  plain_http: false,
  created_at: '2026-10-24T12:00:00Z',
  updated_at: '2026-10-24T12:00:00Z',
};

const button = (wrapper: ReturnType<typeof mount>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text);

describe('RegistriesView', () => {
  it('lists registries', async () => {
    stubApi({ 'GET /registries': { body: [ghcr] } });
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.find('tbody').text()).toContain('GitHub');
    expect(wrapper.find('tbody').text()).toContain('ghcr.io');
    expect(wrapper.find('tbody').text()).toContain('bot');
    expect(wrapper.find('tbody a').attributes('href')).toBe('/registries/1');
  });

  it('adds a registry', async () => {
    let list: unknown[] = [];
    stubApi({
      'GET /registries': () => ({ body: list }),
      'POST /registries': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({
          name: 'GitHub',
          address: 'ghcr.io',
          username: 'bot',
          password: 'hunter2',
          kind: 'generic',
          owners: [],
          plain_http: false,
        });
        list = [ghcr];
        return { status: 201, body: ghcr };
      },
    });
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.text()).toContain('No registries yet.');
    await wrapper.find('input[name=name]').setValue(' GitHub ');
    await wrapper.find('input[name=address]').setValue('ghcr.io');
    await wrapper.find('input[name=username]').setValue('bot');
    await wrapper.find('input[name=password]').setValue('hunter2');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('tbody').text()).toContain('GitHub');
    expect((wrapper.find('input[name=password]').element as HTMLInputElement).value).toBe('');
  });

  it('edits a registry without sending an unchanged password', async () => {
    const forge = { ...ghcr, kind: 'forgejo', owners: ['zefir'], plain_http: true };
    stubApi({
      'GET /registries': { body: [forge] },
      'PUT /registries/1': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({
          name: 'GitHub CR',
          address: 'ghcr.io',
          username: 'bot',
          kind: 'forgejo',
          owners: ['zefir', 'team'],
          plain_http: true,
        });
        return { body: { ...forge, name: 'GitHub CR' } };
      },
    });
    const wrapper = mountView();
    await flushPromises();
    await button(wrapper, 'Edit')?.trigger('click');
    expect(wrapper.text()).toContain('Edit GitHub');
    expect(wrapper.find('input[name=password]').attributes('required')).toBeUndefined();
    expect((wrapper.find('select[name=kind]').element as HTMLSelectElement).value).toBe('forgejo');
    expect((wrapper.find('input[name=owners]').element as HTMLInputElement).value).toBe('zefir');
    expect(wrapper.find('.warn').exists()).toBe(true);
    await wrapper.find('input[name=name]').setValue('GitHub CR');
    await wrapper.find('input[name=owners]').setValue('zefir, team');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Add a registry');
  });

  it('asks for the password again when the username or address changes', async () => {
    const puts: unknown[] = [];
    stubApi({
      'GET /registries': { body: [ghcr] },
      'PUT /registries/1': (init) => {
        puts.push(JSON.parse(String(init?.body)));
        return { body: { ...ghcr, username: 'bot2' } };
      },
    });
    const wrapper = mountView();
    await flushPromises();
    await button(wrapper, 'Edit')?.trigger('click');
    await wrapper.find('input[name=username]').setValue('bot2');
    expect(wrapper.find('input[name=password]').attributes('required')).toBeDefined();
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toContain('Enter the password again');
    expect(puts).toEqual([]);

    await wrapper.find('input[name=password]').setValue('new');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(puts).toEqual([
      {
        name: 'GitHub',
        address: 'ghcr.io',
        username: 'bot2',
        password: 'new',
        kind: 'generic',
        owners: [],
        plain_http: false,
      },
    ]);
    expect(wrapper.text()).toContain('Add a registry');
  });

  it('adds a Forgejo registry browsed over plain HTTP', async () => {
    const posts: unknown[] = [];
    stubApi({
      'GET /registries': { body: [] },
      'POST /registries': (init) => {
        posts.push(JSON.parse(String(init?.body)));
        return { status: 201, body: ghcr };
      },
    });
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.find('input[name=owners]').exists()).toBe(false);
    expect(wrapper.find('.warn').exists()).toBe(false);
    await wrapper.find('input[name=name]').setValue('Forge');
    await wrapper.find('input[name=address]').setValue('git.lan:3000');
    await wrapper.find('input[name=username]').setValue('bot');
    await wrapper.find('input[name=password]').setValue('tok');
    await wrapper.find('select[name=kind]').setValue('forgejo');
    await wrapper.find('input[name=owners]').setValue(' zefir  team ');
    await wrapper.find('input[name=plain_http]').setValue(true);
    expect(wrapper.find('.warn').text()).toContain('unencrypted');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(posts).toEqual([
      {
        name: 'Forge',
        address: 'git.lan:3000',
        username: 'bot',
        password: 'tok',
        kind: 'forgejo',
        owners: ['zefir', 'team'],
        plain_http: true,
      },
    ]);
  });

  it('asks for the password again when plain HTTP changes', async () => {
    stubApi({ 'GET /registries': { body: [ghcr] } });
    const wrapper = mountView();
    await flushPromises();
    await button(wrapper, 'Edit')?.trigger('click');
    await wrapper.find('input[name=plain_http]').setValue(true);
    expect(wrapper.find('input[name=password]').attributes('required')).toBeDefined();
  });

  it('shows an API error', async () => {
    stubApi({
      'GET /registries': { body: [] },
      'POST /registries': {
        status: 409,
        body: { error: 'a registry with that name or address already exists' },
      },
    });
    const wrapper = mountView();
    await flushPromises();
    await wrapper.find('input[name=name]').setValue('x');
    await wrapper.find('input[name=address]').setValue('ghcr.io');
    await wrapper.find('input[name=username]').setValue('u');
    await wrapper.find('input[name=password]').setValue('p');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('a registry with that name or address already exists');
  });

  it('deletes after confirming', async () => {
    let list: unknown[] = [ghcr];
    const fetch = stubApi({
      'GET /registries': () => ({ body: list }),
      'DELETE /registries/1': () => {
        list = [];
        return { status: 204 };
      },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const wrapper = mountView();
    await flushPromises();
    await button(wrapper, 'Delete')?.trigger('click');
    await flushPromises();
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
    await button(wrapper, 'Delete')?.trigger('click');
    await flushPromises();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).toContain('No registries yet.');
  });
});
