import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, stubSockets } from '../test-utils';
import StackNewView from './StackNewView.vue';

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
  await router.push('/env/1/new-stack');
  const wrapper = mount(StackNewView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

describe('StackNewView', () => {
  it('checks the files, creates the stack and opens it', async () => {
    let created: unknown;
    stubApi({
      'POST /stacks/check': { body: { errors: [] } },
      'POST /environments/1/stacks': (init) => {
        created = JSON.parse(init!.body as string);
        return { status: 201, body: {} };
      },
    });
    const { wrapper, router } = await mountView();
    await wrapper.find('input').setValue('web');
    await wrapper.findAll('textarea')[1]!.setValue('TAG=1\n');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(created).toEqual({
      name: 'web',
      compose: 'services:\n  app:\n    image: nginx:alpine\n    restart: unless-stopped\n',
      env: 'TAG=1\n',
    });
    expect(router.currentRoute.value.fullPath).toBe('/env/1/stacks/web');
  });

  it('does not create a stack with YAML errors', async () => {
    const fetch = stubApi({});
    const { wrapper } = await mountView();
    await wrapper.find('input').setValue('web');
    await wrapper.findAll('textarea')[0]!.setValue('services: [\n');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('shows the server error', async () => {
    stubApi({
      'POST /stacks/check': { body: { errors: [] } },
      'POST /environments/1/stacks': { status: 409, body: { error: 'a stack of that name exists' } },
    });
    const { wrapper } = await mountView();
    await wrapper.find('input').setValue('web');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toBe('a stack of that name exists');
  });
});
