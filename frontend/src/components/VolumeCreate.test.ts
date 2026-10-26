import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import VolumeCreate from './VolumeCreate.vue';

afterEach(() => {
  vi.unstubAllGlobals();
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
});
