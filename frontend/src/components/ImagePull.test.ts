import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import ImagePull from './ImagePull.vue';

afterEach(() => {
  vi.unstubAllGlobals();
});

const lines = (...events: unknown[]) => events.map((e) => JSON.stringify(e)).join('\n') + '\n';

async function pull(raw: string, status = 200) {
  const fetch = stubApi({ 'POST /environments/1/images/pull': { status, raw } });
  const wrapper = mount(ImagePull, { props: { env: '1' } });
  await wrapper.find('input[name=image]').setValue(' nginx:1.29 ');
  await wrapper.find('form').trigger('submit');
  await flushPromises();
  return { wrapper, fetch };
}

describe('ImagePull', () => {
  it('pulls and reports success', async () => {
    const { wrapper, fetch } = await pull(
      lines(
        { id: '1.29', status: 'Pulling from library/nginx' },
        { id: '0123456789ab', status: 'Downloading', current: 500, total: 1000 },
        { done: true },
      ),
    );
    expect(JSON.parse(String(fetch.mock.calls[0]?.[1]?.body))).toEqual({ image: 'nginx:1.29' });
    expect(wrapper.find('[role=status]').text()).toBe('Pulled nginx:1.29.');
    expect(wrapper.emitted('pulled')).toEqual([['nginx:1.29']]);
  });

  it('shows an error from the stream with the progress so far', async () => {
    const { wrapper } = await pull(
      lines(
        { id: '0123456789ab', status: 'Downloading', current: 1, total: 4 },
        { error: 'unauthorized: authentication required' },
      ),
    );
    expect(wrapper.find('[role=alert]').text()).toBe('unauthorized: authentication required');
    expect(wrapper.find('progress').attributes('value')).toBe('25');
    expect(wrapper.emitted('pulled')).toBeUndefined();
  });

  it('shows an error before the stream', async () => {
    const { wrapper } = await pull(JSON.stringify({ error: 'pull failed: manifest unknown' }), 404);
    expect(wrapper.find('[role=alert]').text()).toBe('pull failed: manifest unknown');
  });

  it('reports an unreadable line', async () => {
    const { wrapper } = await pull('not json\n');
    expect(wrapper.find('[role=alert]').text()).toBe('unreadable progress from the server');
  });

  it('reports a stream that ends early', async () => {
    const { wrapper } = await pull(lines({ status: 'Pulling' }));
    expect(wrapper.find('[role=alert]').text()).toBe('the pull ended before it finished');
  });
});
