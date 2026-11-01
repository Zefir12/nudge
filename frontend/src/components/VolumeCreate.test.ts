import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { currentUser } from '../session';
import { admin, stubApi, user } from '../test-utils';
import VolumeCreate from './VolumeCreate.vue';

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  currentUser.value = null;
});

const media = {
  name: 'media',
  driver: 'local',
  mountpoint: '',
  scope: 'local',
  created_at: null,
  labels: {},
};

describe('VolumeCreate', () => {
  it('creates a volume with options and labels', async () => {
    const fetch = stubApi({ 'POST /environments/1/volumes': { status: 201, body: media } });
    const wrapper = mount(VolumeCreate, { props: { env: '1' } });
    await wrapper.find('input[name=name]').setValue(' media ');
    await wrapper.find('textarea[name=options]').setValue('type=nfs\no=addr=10.0.0.5,rw\n');
    await wrapper.find('textarea[name=labels]').setValue('app=web');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(JSON.parse(String(fetch.mock.calls[0]?.[1]?.body))).toEqual({
      name: 'media',
      options: { type: 'nfs', o: 'addr=10.0.0.5,rw' },
      labels: { app: 'web' },
    });
    expect(wrapper.find('[role=status]').text()).toBe('Created volume media.');
    expect(wrapper.emitted('created')).toEqual([[media]]);
    expect((wrapper.find('input[name=name]').element as HTMLInputElement).value).toBe('');
  });

  it('shows a bad line without calling the API', async () => {
    const fetch = stubApi({});
    const wrapper = mount(VolumeCreate, { props: { env: '1' } });
    await wrapper.find('textarea[name=labels]').setValue('=x');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(fetch).not.toHaveBeenCalled();
    expect(wrapper.find('[role=alert]').text()).toBe('"=x" has no name before "="');
  });

  it('shows the API error', async () => {
    stubApi({
      'POST /environments/1/volumes': {
        status: 400,
        body: { error: 'the Docker host has no such volume driver' },
      },
    });
    const wrapper = mount(VolumeCreate, { props: { env: '1' } });
    await wrapper.find('input[name=driver]').setValue('nope');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('the Docker host has no such volume driver');
    expect(wrapper.emitted('created')).toBeUndefined();
  });

  it('asks a user only for a name and labels', async () => {
    currentUser.value = user;
    const wrapper = mount(VolumeCreate, { props: { env: '1' } });
    expect(wrapper.find('input[name=name]').attributes('required')).toBeDefined();
    expect(wrapper.find('input[name=driver]').exists()).toBe(false);
    expect(wrapper.find('textarea[name=options]').exists()).toBe(false);
    expect(wrapper.find('textarea[name=labels]').exists()).toBe(true);
  });
});
