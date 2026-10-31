import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import CodeEditor from './CodeEditor.vue';

const mountEditor = (modelValue: string, language: 'yaml' | 'env', extra: Record<string, unknown> = {}) => {
  const wrapper = mount(CodeEditor, {
    props: {
      modelValue,
      language,
      'onUpdate:modelValue': (v: string) => void wrapper.setProps({ modelValue: v }),
      ...extra,
    },
  });
  return wrapper;
};

describe('CodeEditor', () => {
  it('shows the text highlighted behind the textarea and follows edits', async () => {
    const wrapper = mountEditor('image: nginx\n', 'yaml');
    expect(wrapper.find('pre').attributes('aria-hidden')).toBe('true');
    expect(wrapper.find('pre .t-key').text()).toBe('image');
    await wrapper.find('textarea').setValue('# off\n');
    expect(wrapper.emitted('update:modelValue')).toEqual([['# off\n']]);
    expect(wrapper.find('pre .t-key').exists()).toBe(false);
    expect(wrapper.find('pre .t-comment').text()).toBe('# off');
  });

  it('highlights env files', () => {
    const wrapper = mountEditor('A=${B}', 'env');
    expect(wrapper.find('pre .t-key').text()).toBe('A');
    expect(wrapper.find('pre .t-variable').text()).toBe('${B}');
  });

  it('passes attributes and disabled to the textarea and sizes the box', () => {
    const wrapper = mountEditor('', 'yaml', { disabled: true, size: 'tall', 'aria-invalid': true });
    const area = wrapper.find('textarea');
    expect(area.attributes('disabled')).toBeDefined();
    expect(area.attributes('aria-invalid')).toBe('true');
    expect(wrapper.classes()).toEqual(expect.arrayContaining(['code-editor', 'tall', 'disabled']));
  });

  it('keeps the highlight scrolled with the textarea', async () => {
    const wrapper = mountEditor('a: 1\n'.repeat(100), 'yaml');
    const area = wrapper.find('textarea').element;
    area.scrollTop = 120;
    area.scrollLeft = 7;
    await wrapper.find('textarea').trigger('scroll');
    const pre = wrapper.find('pre').element;
    expect([pre.scrollTop, pre.scrollLeft]).toEqual([120, 7]);
  });

  it('gives the highlight the same scrollbars as the textarea', async () => {
    const wrapper = mountEditor('a: 1\n', 'yaml');
    const area = wrapper.find('textarea').element;
    // jsdom has no layout: fake a 10px vertical bar inside 1px borders.
    const box = {
      offsetWidth: 200,
      clientWidth: 188,
      clientLeft: 1,
      offsetHeight: 100,
      clientHeight: 98,
      clientTop: 1,
    };
    for (const [k, v] of Object.entries(box))
      Object.defineProperty(area, k, { configurable: true, value: v });
    await wrapper.find('textarea').setValue('a: 1\n'.repeat(100));
    expect(wrapper.classes()).toContain('vbar');
    expect(wrapper.classes()).not.toContain('hbar');
  });
});
