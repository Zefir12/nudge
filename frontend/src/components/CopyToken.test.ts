import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import CopyToken from './CopyToken.vue';

const props = { label: 'API_TAG +1', values: ['API_TAG=1.4.2', 'WEB_TAG=1.4.2'] };

function stubClipboard(writeText: unknown) {
  vi.stubGlobal('navigator', { clipboard: writeText ? { writeText } : undefined });
}

describe('CopyToken', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
    Reflect.deleteProperty(document, 'execCommand');
  });

  it('shows the short label and names every value when hovered', () => {
    const wrapper = mount(CopyToken, { props });
    expect(wrapper.text()).toBe('API_TAG +1');
    expect(wrapper.attributes('title')).toBe('API_TAG=1.4.2\nWEB_TAG=1.4.2');
  });

  it('copies every value on click, then goes back to the label', async () => {
    vi.useFakeTimers();
    const writeText = vi.fn().mockResolvedValue(undefined);
    stubClipboard(writeText);
    const wrapper = mount(CopyToken, { props });

    await wrapper.trigger('click');
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith('API_TAG=1.4.2\nWEB_TAG=1.4.2');
    expect(wrapper.text()).toBe('Copied');

    await vi.advanceTimersByTimeAsync(1500);
    expect(wrapper.text()).toBe('API_TAG +1');
  });

  it.each([
    { name: 'no clipboard API', writeText: undefined },
    { name: 'a refused clipboard', writeText: vi.fn().mockRejectedValue(new Error('denied')) },
  ])('falls back to the copy command with $name', async ({ writeText }) => {
    stubClipboard(writeText);
    let copied = '';
    document.execCommand = vi.fn(() => {
      copied = (document.activeElement as HTMLTextAreaElement).value;
      return true;
    });
    const wrapper = mount(CopyToken, { props });

    await wrapper.trigger('click');
    await flushPromises();
    expect(copied).toBe('API_TAG=1.4.2\nWEB_TAG=1.4.2');
    expect(wrapper.text()).toBe('Copied');
    expect(document.querySelector('textarea')).toBeNull();
  });

  it('says so when nothing could copy', async () => {
    stubClipboard(undefined);
    document.execCommand = vi.fn(() => false);
    const wrapper = mount(CopyToken, { props });

    await wrapper.trigger('click');
    await flushPromises();
    expect(wrapper.text()).toBe('Copy failed');
  });
});
