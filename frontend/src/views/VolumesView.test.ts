import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi, stubSockets } from '../test-utils';
import VolumesView from './VolumesView.vue';

const vol = (name: string, size: number | null = null, measured: string | null = null) => ({
  name,
  driver: 'local',
  mountpoint: '',
  scope: 'local',
  created_at: null,
  labels: {},
  size,
  size_measured_at: measured,
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
  stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('VolumesView', () => {
  it('shows sizes and sorts by them', async () => {
    const at = '2026-10-26T12:00:00Z';
    stubApi({
      'GET /environments/1/volumes': {
        body: [vol('a', 10, at), vol('b', 3000, at), vol('c'), vol('d', null, at)],
      },
    });
    const wrapper = await mountView();
    const sizes = () => wrapper.findAll('td.size').map((td) => td.text());
    expect(sizes()).toEqual(['10 B', '3.0 KB', '—', '—']);
    const titles = wrapper.findAll('td.size').map((td) => td.attributes('title'));
    expect(titles[2]).toBe('Not measured yet');
    expect(titles[3]).toContain("can't report");

    await button(wrapper, 'Size').trigger('click');
    expect(wrapper.findAll('tbody tr').map((tr) => tr.find('td').text())).toEqual(['b', 'a', 'c', 'd']);
    expect(wrapper.find('th[aria-sort]').attributes('aria-sort')).toBe('descending');
  });

  it('measures sizes now and reloads', async () => {
    let measured = false;
    stubApi({
      'GET /environments/1/volumes': () => ({
        body: [vol('a', measured ? 2048 : null, measured ? '2026-10-26T12:00:00Z' : null)],
      }),
      'POST /environments/1/volumes/usage': () => {
        measured = true;
        return { status: 204 };
      },
    });
    const wrapper = await mountView();
    await button(wrapper, 'Measure sizes').trigger('click');
    await flushPromises();
    expect(wrapper.find('td.size').text()).toBe('2.0 KB');
  });

  it('shows a measurement error', async () => {
    stubApi({
      'GET /environments/1/volumes': { body: [vol('a')] },
      'POST /environments/1/volumes/usage': {
        status: 409,
        body: { error: 'a measurement of this environment is running' },
      },
    });
    const wrapper = await mountView();
    await button(wrapper, 'Measure sizes').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain('running');
  });

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
