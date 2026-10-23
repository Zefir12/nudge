import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import DashboardView from './DashboardView.vue';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('DashboardView', () => {
  it('shows the server version', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('{"status":"ok","version":"v1.2.3"}')),
    );
    const wrapper = mount(DashboardView);
    await flushPromises();
    expect(wrapper.text()).toContain('v1.2.3');
  });

  it('shows an error when the API is down', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('{"error":"boom"}', { status: 500 })),
    );
    const wrapper = mount(DashboardView);
    await flushPromises();
    expect(wrapper.text()).toContain('Unreachable: boom');
  });
});
