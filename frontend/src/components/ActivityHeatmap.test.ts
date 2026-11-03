import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import ActivityHeatmap from './ActivityHeatmap.vue';

const days = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

// at sets the clock; the local date doesn't matter, only the hour.
function at(hour: number) {
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date(2026, 9, 27, hour, 30));
}

describe('ActivityHeatmap', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('draws a cell per hour, shaded against the busiest one, that names its count', () => {
    const counts = Array.from({ length: 7 * 24 }, () => 0);
    counts[24 + 14] = 20; // Tue 14:00
    counts[24 + 15] = 1;
    at(23);
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

  it.each([
    { hour: 0, cells: 6 * 24 + 1 },
    { hour: 13, cells: 6 * 24 + 14 },
    { hour: 23, cells: 7 * 24 },
  ])('ends today at hour $hour', ({ hour, cells }) => {
    at(hour);
    const counts = Array.from({ length: 7 * 24 }, () => 0);
    const wrapper = mount(ActivityHeatmap, { props: { counts, days } });
    const drawn = wrapper.findAll('rect');
    expect(drawn).toHaveLength(cells);
    expect(drawn.at(-1)!.find('title').text()).toBe(`Sun ${String(hour).padStart(2, '0')}:00, 0 events`);
  });

  it('leaves the hours yet to come out of the shading', () => {
    at(13);
    const counts = Array.from({ length: 7 * 24 }, () => 0);
    counts[0] = 2;
    counts[6 * 24 + 20] = 100;
    const wrapper = mount(ActivityHeatmap, { props: { counts, days } });
    expect(wrapper.findAll('rect')[0]!.attributes('fill-opacity')).toBe('1');
  });
});
