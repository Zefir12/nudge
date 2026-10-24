import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { ImageDetail } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import ImageView from './ImageView.vue';

const detail: ImageDetail = {
  id: 'sha256:0123456789abcdef',
  tags: ['nginx:1.29', 'nginx:latest'],
  digests: [],
  created_at: '2026-10-01T12:00:00Z',
  size: 1000,
  author: '',
  comment: '',
  architecture: 'amd64',
  variant: '',
  os: 'linux',
  config: {
    user: '',
    env: [],
    cmd: [],
    entrypoint: [],
    working_dir: '',
    exposed_ports: [],
    volumes: [],
    labels: {},
  },
  layers: [],
};

async function mountAt(path: string) {
  const router = createAppRouter(createMemoryHistory());
  await router.push(path);
  const wrapper = mount(ImageView, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

const removeButton = (wrapper: ReturnType<typeof mount>) =>
  wrapper.findAll('button').find((b) => b.text() === 'Remove')!;

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ImageView', () => {
  it('removes the image and goes back to the list', async () => {
    const fetch = stubApi({
      [`GET /environments/1/images/${encodeURIComponent(detail.id)}`]: { body: detail },
      [`DELETE /environments/1/images/${encodeURIComponent(detail.id)}`]: { status: 204 },
      'GET /environments/1/images': { body: [] },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper, router } = await mountAt(`/env/1/images/${detail.id}`);
    await removeButton(wrapper).trigger('click');
    await flushPromises();
    expect(fetch).toHaveBeenCalledWith(
      `/api/v1/environments/1/images/${encodeURIComponent(detail.id)}`,
      expect.objectContaining({ method: 'DELETE' }),
    );
    expect(router.currentRoute.value.fullPath).toBe('/env/1/images');
  });

  it('removes with force and shows a conflict', async () => {
    const fetch = stubApi({
      [`GET /environments/1/images/${encodeURIComponent(detail.id)}`]: { body: detail },
      [`DELETE /environments/1/images/${encodeURIComponent(detail.id)}?force=true`]: {
        status: 409,
        body: { error: 'the image is in use' },
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper, router } = await mountAt(`/env/1/images/${detail.id}`);
    await wrapper.find('input[type=checkbox]').setValue(true);
    await removeButton(wrapper).trigger('click');
    await flushPromises();
    expect(fetch.mock.calls.some(([url]) => String(url).endsWith('?force=true'))).toBe(true);
    expect(wrapper.find('[role=alert]').text()).toBe('the image is in use');
    expect(router.currentRoute.value.fullPath).toBe(`/env/1/images/${detail.id}`);
  });

  it('does nothing without confirmation', async () => {
    const fetch = stubApi({
      [`GET /environments/1/images/${encodeURIComponent(detail.id)}`]: { body: detail },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    const { wrapper } = await mountAt(`/env/1/images/${detail.id}`);
    await removeButton(wrapper).trigger('click');
    await flushPromises();
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
  });
});
