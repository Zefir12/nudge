import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import SetupView from './SetupView.vue';

beforeEach(() => {
  currentUser.value = null;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function mountSetup() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/setup');
  const wrapper = mount(SetupView, { global: { plugins: [router] } });
  return { router, wrapper };
}

async function fill(wrapper: ReturnType<typeof mount>, password: string, repeat: string) {
  await wrapper.find('input[name=username]').setValue('admin');
  await wrapper.find('input[name=password]').setValue(password);
  await wrapper.find('input[name=repeat]').setValue(repeat);
  await wrapper.find('form').trigger('submit');
  await flushPromises();
}

describe('SetupView', () => {
  it('creates the admin and opens the dashboard', async () => {
    const fetch = stubApi({
      'GET /setup': { body: { needed: true } },
      'POST /setup': { status: 201, body: admin },
      'GET /health': { body: { status: 'ok', version: 'v1' } },
    });
    const { router, wrapper } = await mountSetup();
    await fill(wrapper, 'longenough', 'longenough');
    expect(fetch).toHaveBeenCalledWith('/api/v1/setup', expect.objectContaining({ method: 'POST' }));
    expect(currentUser.value).toEqual(admin);
    expect(router.currentRoute.value.name).toBe('dashboard');
  });

  it.each([
    { name: 'short password', password: 'short', repeat: 'short', want: 'at least 8 characters' },
    { name: 'mismatch', password: 'longenough', repeat: 'different1', want: 'do not match' },
  ])('refuses a $name before calling the API', async ({ password, repeat, want }) => {
    const fetch = stubApi({ 'GET /setup': { body: { needed: true } } });
    const { wrapper } = await mountSetup();
    await fill(wrapper, password, repeat);
    expect(wrapper.find('[role=alert]').text()).toContain(want);
    expect(fetch).not.toHaveBeenCalledWith('/api/v1/setup', expect.objectContaining({ method: 'POST' }));
  });

  it('shows the server error', async () => {
    stubApi({
      'GET /setup': { body: { needed: true } },
      'POST /setup': { status: 400, body: { error: 'username must be 1 to 64 bytes' } },
    });
    const { wrapper } = await mountSetup();
    await fill(wrapper, 'longenough', 'longenough');
    expect(wrapper.find('[role=alert]').text()).toBe('username must be 1 to 64 bytes');
  });

  it('goes to login when someone else finished setup', async () => {
    stubApi({
      'GET /setup': { body: { needed: true } },
      'POST /setup': { status: 409, body: { error: 'setup is already done' } },
    });
    const { router, wrapper } = await mountSetup();
    stubApi({
      'POST /setup': { status: 409, body: { error: 'setup is already done' } },
      'GET /setup': { body: { needed: false } },
    });
    await fill(wrapper, 'longenough', 'longenough');
    expect(router.currentRoute.value.name).toBe('login');
  });
});
