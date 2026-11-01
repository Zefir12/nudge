import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import UsageHistory from './UsageHistory.vue';

describe('UsageHistory', () => {
  it('charts CPU and memory under their current values', () => {
    const wrapper = mount(UsageHistory, {
      props: { cpu: [0, 50, 100], mem: [20, 20], cpuNow: 4.25, memNow: 61.6 },
    });
    expect(wrapper.findAll('.head strong').map((s) => s.text())).toEqual(['4.3%', '62%']);
    // 0% sits at the bottom of the 30-high box, 100% near the top.
    expect(wrapper.find('.line.cpu').attributes('d')).toBe('M0.00,29.00 L50.00,15.50 L100.00,2.00');
    expect(wrapper.find('.area.cpu').attributes('d')).toMatch(/L100,30 L0,30 Z$/);
  });

  it('shows a dash while the current value is unknown', () => {
    const wrapper = mount(UsageHistory, { props: { cpu: [1], mem: [1], cpuNow: null, memNow: null } });
    expect(wrapper.findAll('.head strong').map((s) => s.text())).toEqual(['–', '–']);
  });
});
