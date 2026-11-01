import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { currentUser } from '../session';
import { admin, stubApi, user } from '../test-utils';
import NetworkCreate from './NetworkCreate.vue';

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  currentUser.value = null;
});

describe('NetworkCreate', () => {
  it('creates a network with a subnet and flags', async () => {
    const fetch = stubApi({
      'POST /environments/1/networks': { status: 201, body: { id: 'n1', warnings: ['a warning'] } },
    });
    const wrapper = mount(NetworkCreate, { props: { env: '1' } });
    await wrapper.find('input[name=name]').setValue('apps');
    await wrapper.find('input[name=subnet]').setValue('172.30.0.0/16');
    await wrapper.find('input[name=gateway]').setValue('172.30.0.1');
    const [internal] = wrapper.findAll('input[type=checkbox]');
    await internal!.setValue(true);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(JSON.parse(String(fetch.mock.calls[0]?.[1]?.body))).toEqual({
      name: 'apps',
      subnet: '172.30.0.0/16',
      gateway: '172.30.0.1',
      internal: true,
      attachable: false,
      ipv6: false,
      options: {},
      labels: {},
    });
    expect(wrapper.find('[role=status]').text()).toBe('Created network apps. a warning');
    expect(wrapper.emitted('created')).toEqual([['n1']]);
    expect((wrapper.find('input[name=subnet]').element as HTMLInputElement).value).toBe('');
  });

  it('shows the API error', async () => {
    stubApi({
      'POST /environments/1/networks': {
        status: 409,
        body: { error: 'a network with this name already exists' },
      },
    });
    const wrapper = mount(NetworkCreate, { props: { env: '1' } });
    await wrapper.find('input[name=name]').setValue('front');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('a network with this name already exists');
  });

  it('leaves out what a user may not set', async () => {
    currentUser.value = user;
    const wrapper = mount(NetworkCreate, { props: { env: '1' } });
    for (const name of ['driver', 'subnet', 'gateway', 'ip_range']) {
      expect(wrapper.find(`input[name=${name}]`).exists()).toBe(false);
    }
    expect(wrapper.find('textarea[name=options]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('IPv6');
    expect(wrapper.text()).toContain('Internal');
  });
});
