import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import VolumeView from './VolumeView.vue';

const data = {
  name: 'data',
  driver: 'local',
  mountpoint: '/x',
  scope: 'local',
  created_at: null,
  labels: {},
};

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/env/1/volumes/data');
  const wrapper = mount(VolumeView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

const remove = (wrapper: ReturnType<typeof mount>) =>
  wrapper.findAll('button').find((b) => b.text() === 'Remove')!;

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('VolumeView', () => {
  it('removes the volume with force and goes back to the list', async () => {
    stubApi({
      'GET /environments/1/volumes/data': { body: data },
      'DELETE /environments/1/volumes/data?force=true': { status: 204 },
      'GET /environments/1/volumes': { body: [] },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper, router } = await mountView();
    await wrapper.find('input[type=checkbox]').setValue(true);
    await remove(wrapper).trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('volumes');
  });

  it('shows why the volume was not removed', async () => {
    stubApi({
      'GET /environments/1/volumes/data': { body: data },
      'DELETE /environments/1/volumes/data': {
        status: 409,
        body: { error: 'the volume is in use by a container' },
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper, router } = await mountView();
    await remove(wrapper).trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('the volume is in use by a container');
    expect(router.currentRoute.value.name).toBe('volume');
  });
});
