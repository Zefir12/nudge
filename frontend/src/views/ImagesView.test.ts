import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import ImagesView from './ImagesView.vue';

const nginx = {
  id: 'sha256:0123456789abcdef',
  tags: ['nginx:1.29'],
  digests: [],
  created_at: null,
  size: 1000,
  labels: {},
  dangling: false,
};

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/env/1/images');
  const wrapper = mount(ImagesView, { global: { plugins: [router] } });
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

describe('ImagesView', () => {
  it('prunes untagged images and reloads the list', async () => {
    let images: unknown[] = [nginx, { ...nginx, id: 'sha256:fff', tags: [], dangling: true }];
    stubApi({
      'GET /environments/1/images': () => ({ body: images }),
      'POST /environments/1/images/prune': () => {
        images = [nginx];
        return { body: { deleted: ['sha256:fff'], untagged: [], space_reclaimed: 2_500_000 } };
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = await mountView();
    expect(wrapper.findAll('tbody tr')).toHaveLength(2);
    await button(wrapper, 'Prune untagged').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=status]').text()).toContain('reclaimed 2.5 MB');
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
  });

  it('prunes all unused images', async () => {
    const fetch = stubApi({
      'GET /environments/1/images': { body: [nginx] },
      'POST /environments/1/images/prune?all=true': {
        body: { deleted: [], untagged: [], space_reclaimed: 0 },
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = await mountView();
    await button(wrapper, 'Prune unused').trigger('click');
    await flushPromises();
    expect(fetch.mock.calls.some(([url]) => String(url).endsWith('/prune?all=true'))).toBe(true);
    expect(wrapper.find('[role=status]').text()).toBe('Nothing to remove.');
  });

  it('reloads the list after a pull', async () => {
    let images: unknown[] = [];
    stubApi({
      'GET /environments/1/images': () => ({ body: images }),
      'POST /environments/1/images/pull': () => {
        images = [nginx];
        return { raw: '{"done":true}\n' };
      },
    });
    const wrapper = await mountView();
    expect(wrapper.text()).toContain('No images.');
    await wrapper.find('input[name=image]').setValue('nginx:1.29');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
  });
});
