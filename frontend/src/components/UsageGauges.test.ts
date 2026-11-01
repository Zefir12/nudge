import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import UsageGauges from './UsageGauges.vue';

describe('UsageGauges', () => {
  it.each([
    { name: 'a known percent', percent: 42.4, text: '42%', ring: '42.4 100' },
    { name: 'no percent yet', percent: null, text: '–', ring: undefined },
  ])('shows $name', ({ percent, text, ring }) => {
    const wrapper = mount(UsageGauges, {
      props: { gauges: [{ label: 'CPU', percent, detail: '8 cores', tone: 'cpu' }] },
    });
    expect(wrapper.find('text').text()).toBe(text);
    expect(wrapper.find('.ring').exists()).toBe(ring !== undefined);
    if (ring) expect(wrapper.find('.ring').attributes('stroke-dasharray')).toBe(ring);
    expect(wrapper.find('.label').text()).toBe('CPU');
    expect(wrapper.find('.label .icon').classes()).toContain('cpu');
    expect(wrapper.find('.detail').text()).toBe('8 cores');
  });
});
