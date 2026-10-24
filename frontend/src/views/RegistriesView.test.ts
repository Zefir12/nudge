import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import RegistriesView from './RegistriesView.vue';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const ghcr = {
  id: 1,
  name: 'GitHub',
  address: 'ghcr.io',
  username: 'bot',
  created_at: '2026-10-24T12:00:00Z',
  updated_at: '2026-10-24T12:00:00Z',
};

const button = (wrapper: ReturnType<typeof mount>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text);

describe('RegistriesView', () => {
  it('lists registries', async () => {
    stubApi({ 'GET /registries': { body: [ghcr] } });
    const wrapper = mount(RegistriesView);
    await flushPromises();
    expect(wrapper.find('tbody').text()).toContain('GitHub');
    expect(wrapper.find('tbody').text()).toContain('ghcr.io');
    expect(wrapper.find('tbody').text()).toContain('bot');
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
        });
        list = [ghcr];
        return { status: 201, body: ghcr };
      },
    });
    const wrapper = mount(RegistriesView);
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
    stubApi({
      'GET /registries': { body: [ghcr] },
      'PUT /registries/1': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({
          name: 'GitHub',
          address: 'ghcr.io',
          username: 'bot2',
        });
        return { body: { ...ghcr, username: 'bot2' } };
      },
    });
    const wrapper = mount(RegistriesView);
    await flushPromises();
    await button(wrapper, 'Edit')?.trigger('click');
    expect(wrapper.text()).toContain('Edit GitHub');
    await wrapper.find('input[name=username]').setValue('bot2');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Add a registry');
  });

  it('shows an API error', async () => {
    stubApi({
      'GET /registries': { body: [] },
      'POST /registries': {
        status: 409,
        body: { error: 'a registry with that name or address already exists' },
      },
    });
    const wrapper = mount(RegistriesView);
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
    const wrapper = mount(RegistriesView);
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
