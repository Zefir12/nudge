import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import type { Environment } from '../api/client';
import EnvStatus from './EnvStatus.vue';

const base: Environment = {
  id: 2,
  name: 'nas',
  kind: 'agent',
  online: false,
  created_at: '',
  last_seen_at: null,
};

describe('EnvStatus', () => {
  it.each([
    {
      name: 'online',
      env: { ...base, online: true, agent: { enrolled: true, version: '' } },
      want: 'online',
    },
    {
      name: 'never enrolled',
      env: { ...base, agent: { enrolled: false, version: '' } },
      want: 'waiting for agent',
    },
    { name: 'agent gone', env: { ...base, agent: { enrolled: true, version: '' } }, want: 'offline' },
    { name: 'local down', env: { ...base, kind: 'local' as const }, want: 'offline' },
  ])('$name', ({ env, want }) => {
    expect(mount(EnvStatus, { props: { env } }).find('.badge').text()).toBe(want);
  });

  it('shows when an offline host was last seen', () => {
    const env = { ...base, last_seen_at: '2026-10-24T13:00:00Z', agent: { enrolled: true, version: '' } };
    expect(mount(EnvStatus, { props: { env } }).text()).toContain('last seen');
    expect(mount(EnvStatus, { props: { env: { ...env, online: true } } }).text()).not.toContain('last seen');
  });
});
