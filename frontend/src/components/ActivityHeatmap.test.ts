import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import ActivityHeatmap from './ActivityHeatmap.vue';

const days = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

describe('ActivityHeatmap', () => {
  it('draws a cell per hour, shaded against the busiest one, that names its count', () => {
    const counts = Array.from({ length: 7 * 24 }, () => 0);
    counts[24 + 14] = 20; // Tue 14:00
    counts[24 + 15] = 1;
    const wrapper = mount(ActivityHeatmap, { props: { counts, days } });
    const cells = wrapper.findAll('rect');
    expect(cells).toHaveLength(7 * 24);
    expect(wrapper.findAll('.days li').map((d) => d.text())).toEqual(days);

    const busiest = cells[24 + 14]!;
    expect(busiest.attributes('x')).toBe('14.08');
    expect(busiest.attributes('y')).toBe('1.08');
    expect(busiest.attributes('fill-opacity')).toBe('1');
    expect(busiest.find('title').text()).toBe('Tue 14:00, 20 events');
    expect(cells[24 + 15]!.find('title').text()).toBe('Tue 15:00, 1 event');
    expect(cells[0]!.attributes('fill-opacity')).toBe('0.08');
  });
});
