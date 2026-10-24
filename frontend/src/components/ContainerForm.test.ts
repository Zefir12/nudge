import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import type { ContainerSpec } from '../api/client';
import ContainerForm from './ContainerForm.vue';

const spec: ContainerSpec = {
  image: 'nginx:1.29',
  name: 'web',
  command: ['nginx', '-g', 'daemon off;'],
  env: ['A=1'],
  ports: [{ host_ip: '', host_port: 8080, container_port: 80, protocol: 'tcp' }],
  mounts: [{ type: 'volume', source: 'data', target: '/data', read_only: false }],
  networks: ['front', 'back'],
  restart_policy: 'unless-stopped',
  labels: { app: 'web' },
};

const mountForm = (props: Partial<InstanceType<typeof ContainerForm>['$props']> = {}) =>
  mount(ContainerForm, { props: { fields: {}, busy: false, submitLabel: 'Create', ...props } });

describe('ContainerForm', () => {
  it('submits the settings it was given unchanged', async () => {
    const wrapper = mountForm({ initial: spec });
    await wrapper.find('form').trigger('submit');
    expect(wrapper.emitted('submit')).toEqual([[spec]]);
  });

  it('builds a spec from the fields', async () => {
    const wrapper = mountForm();
    await wrapper.find('input[name=image]').setValue(' redis:8 ');
    await wrapper.find('textarea[name=command]').setValue('redis-server\n--appendonly\nyes\n');
    await wrapper.find('textarea[name=env]').setValue('A=1\n\nB=2');
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Add port')!
      .trigger('click');
    await wrapper.find('input[name="ports.0.container_port"]').setValue(6379);
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Add volume or bind')!
      .trigger('click');
    await wrapper.find('select[name="mounts.0.type"]').setValue('bind');
    await wrapper.find('input[name="mounts.0.source"]').setValue('/srv/redis');
    await wrapper.find('input[name="mounts.0.target"]').setValue('/data');
    await wrapper.find('input[name=networks]').setValue('back, ');
    await wrapper.find('select[name=restart_policy]').setValue('always');
    await wrapper.find('textarea[name=labels]').setValue('tier=cache');
    await wrapper.find('form').trigger('submit');
    expect(wrapper.emitted('submit')).toEqual([
      [
        {
          image: 'redis:8',
          name: '',
          command: ['redis-server', '--appendonly', 'yes'],
          env: ['A=1', 'B=2'],
          ports: [{ host_ip: '', host_port: 0, container_port: 6379, protocol: 'tcp' }],
          mounts: [{ type: 'bind', source: '/srv/redis', target: '/data', read_only: false }],
          networks: ['back'],
          restart_policy: 'always',
          labels: { tier: 'cache' },
        },
      ],
    ]);
  });

  it('shows the API messages next to their fields', () => {
    const wrapper = mountForm({
      initial: spec,
      fields: { image: 'required', 'env.0': 'must be NAME=value', 'ports.0.host_port': 'must be 0 to 65535' },
    });
    const errors = wrapper.findAll('.error').map((e) => e.text());
    expect(errors).toEqual(['required', 'Line 1: must be NAME=value', 'host port: must be 0 to 65535']);
  });

  it('refuses a bad label line', async () => {
    const wrapper = mountForm({ initial: spec });
    await wrapper.find('textarea[name=labels]').setValue('=x');
    await wrapper.find('form').trigger('submit');
    expect(wrapper.emitted('submit')).toBeUndefined();
    expect(wrapper.find('.error').text()).toBe('"=x" has no name before "="');
  });
});
