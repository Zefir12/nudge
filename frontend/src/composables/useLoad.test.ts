import { flushPromises } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vite-plus/test';
import { ref } from 'vue';

import { useLoad } from './useLoad';

describe('useLoad', () => {
  it('skips sources with an empty part, as when leaving the page', async () => {
    const env = ref('1');
    const id = ref('abc');
    const fetch = vi.fn(async (s: readonly [string, string]) => s.join('/'));
    const { data } = useLoad(() => [env.value, id.value] as const, fetch);
    await flushPromises();
    expect(data.value).toBe('1/abc');
    id.value = '';
    await flushPromises();
    env.value = '';
    await flushPromises();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('keeps only the newest answer', async () => {
    const env = ref('1');
    const answers: Record<string, (v: string) => void> = {};
    const { data } = useLoad(env, (e) => new Promise<string>((resolve) => (answers[e] = resolve)));
    env.value = '2';
    await flushPromises();
    answers['2']!('two');
    answers['1']!('one');
    await flushPromises();
    expect(data.value).toBe('two');
  });
});
