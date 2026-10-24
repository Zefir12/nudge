import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, stubSockets } from '../test-utils';
import StacksView from './StacksView.vue';

beforeEach(() => {
  currentUser.value = admin;
  stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/env/1/stacks');
  const wrapper = mount(StacksView, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe('StacksView', () => {
  it('lists stacks with their status', async () => {
    stubApi({
      'GET /environments/1/stacks': {
        body: [
          { name: 'db', containers: 0, running: 0 },
          { name: 'web', containers: 3, running: 2 },
          { name: 'zz', containers: 1, running: 1 },
        ],
      },
    });
    const wrapper = await mountView();
    const rows = wrapper.findAll('tbody tr').map((r) => r.findAll('td').map((c) => c.text()));
    expect(rows).toEqual([
      ['db', 'down', '0 / 0 running'],
      ['web', 'partly running', '2 / 3 running'],
      ['zz', 'running', '1 / 1 running'],
    ]);
    expect(wrapper.find('tbody a').attributes('href')).toBe('/env/1/stacks/db');
    expect(wrapper.find('a.button').attributes('href')).toBe('/env/1/new-stack');

    await wrapper.find('input[type="search"]').setValue('we');
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
  });

  it('marks stacks with a container that has an update', async () => {
    const ctr = {
      image: 'nginx:1.29',
      image_id: 'sha256:n1',
      labels: { 'com.docker.compose.project': 'web' },
    };
    stubApi({
      'GET /environments/1/stacks': {
        body: [
          { name: 'db', containers: 1, running: 1 },
          { name: 'web', containers: 1, running: 1 },
        ],
      },
      'GET /environments/1/containers': {
        body: [
          ctr,
          { image: 'postgres:18', image_id: 'sha256:p1', labels: { 'com.docker.compose.project': 'db' } },
        ],
      },
      'GET /environments/1/updates': {
        body: [
          {
            image: 'nginx:1.29',
            image_id: 'sha256:n1',
            status: 'available',
            local_digest: 'sha256:a',
            remote_digest: 'sha256:b',
            error: '',
            checked_at: '2026-10-24T12:00:00Z',
          },
          {
            image: 'postgres:18',
            image_id: 'sha256:p1',
            status: 'unknown',
            local_digest: '',
            remote_digest: '',
            error: 'x',
            checked_at: '2026-10-24T12:00:00Z',
          },
        ],
      },
    });
    const wrapper = await mountView();
    const rows = wrapper.findAll('tbody tr');
    expect(rows[0]!.find('.update').exists()).toBe(false);
    expect(rows[1]!.find('.update').exists()).toBe(true);
  });

  it('says when there are none', async () => {
    stubApi({ 'GET /environments/1/stacks': { body: [] } });
    const wrapper = await mountView();
    expect(wrapper.text()).toContain('No stacks yet.');
  });
});
