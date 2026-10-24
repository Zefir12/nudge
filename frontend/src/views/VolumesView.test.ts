import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import VolumesView from './VolumesView.vue';

const vol = (name: string) => ({
  name,
  driver: 'local',
  mountpoint: '',
  scope: 'local',
  created_at: null,
  labels: {},
});

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/env/1/volumes');
  const wrapper = mount(VolumesView, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

const button = (wrapper: ReturnType<typeof mount>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text)!;

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('VolumesView', () => {
  it('prunes anonymous volumes and reloads the list', async () => {
    let volumes = [vol('data'), vol('0123abcd')];
    stubApi({
      'GET /environments/1/volumes': () => ({ body: volumes }),
      'POST /environments/1/volumes/prune': () => {
        volumes = [vol('data')];
        return { body: { deleted: ['0123abcd'], space_reclaimed: 2_500_000 } };
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = await mountView();
    expect(wrapper.findAll('tbody tr')).toHaveLength(2);
    await button(wrapper, 'Prune anonymous').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=status]').text()).toBe('Removed 1 volumes, reclaimed 2.5 MB.');
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
  });

  it('prunes all unused volumes only after confirming', async () => {
    const fetch = stubApi({
      'GET /environments/1/volumes': { body: [] },
      'POST /environments/1/volumes/prune?all=true': { body: { deleted: [], space_reclaimed: 0 } },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const wrapper = await mountView();
    await button(wrapper, 'Prune unused').trigger('click');
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
    confirm.mockReturnValue(true);
    await button(wrapper, 'Prune unused').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=status]').text()).toBe('Nothing to remove.');
  });

  it('reloads the list after creating a volume', async () => {
    let volumes: unknown[] = [];
    stubApi({
      'GET /environments/1/volumes': () => ({ body: volumes }),
      'POST /environments/1/volumes': () => {
        volumes = [vol('media')];
        return { status: 201, body: vol('media') };
      },
    });
    const wrapper = await mountView();
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
  });
});
