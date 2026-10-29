import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import DiskUsage from './DiskUsage.vue';

describe('DiskUsage', () => {
  it('splits the used part of the disk by what takes it', () => {
    const wrapper = mount(DiskUsage, {
      props: {
        total: 100e9,
        parts: [
          { label: 'Volumes', bytes: 30e9 },
          { label: 'Images', bytes: 20e9 },
        ],
        reclaimable: '5 GB reclaimable',
      },
    });
    expect(wrapper.find('.big').text()).toBe('50.0 GB of 100.0 GB used');
    expect(wrapper.find('svg').attributes('aria-label')).toBe('Disk 50% used');
    const rects = wrapper.findAll('rect');
    expect(rects.map((r) => r.attributes('x'))).toEqual(['0', '30', '50']);
    expect(rects[2]?.classes()).toContain('free');
    expect(wrapper.findAll('.legend li').map((l) => l.text())).toEqual(['Volumes 30.0 GB', 'Images 20.0 GB']);
    expect(wrapper.find('.reclaim').text()).toBe('5 GB reclaimable');
  });

  it('shows the parts alone without the disk size', () => {
    const wrapper = mount(DiskUsage, {
      props: {
        total: null,
        parts: [
          { label: 'Volumes', bytes: 3e9 },
          { label: 'Images', bytes: 1e9 },
        ],
      },
    });
    expect(wrapper.find('.big').text()).toBe('4.0 GB used by Docker');
    const rects = wrapper.findAll('rect');
    expect(rects.map((r) => r.attributes('x'))).toEqual(['0', '75']);
    expect(wrapper.find('.free').exists()).toBe(false);
  });

  it('clips parts that add up to more than the disk', () => {
    const wrapper = mount(DiskUsage, {
      props: {
        total: 10,
        parts: [
          { label: 'Volumes', bytes: 8 },
          { label: 'Images', bytes: 8 },
        ],
      },
    });
    expect(wrapper.findAll('rect').map((r) => r.attributes('x'))).toEqual(['0', '80', '100']);
    expect(wrapper.find('svg').attributes('aria-label')).toBe('Disk 100% used');
  });

  it('leaves out the reclaimable note without one', () => {
    const wrapper = mount(DiskUsage, { props: { total: 10, parts: [{ label: 'Images', bytes: 1 }] } });
    expect(wrapper.find('.reclaim').exists()).toBe(false);
  });
});
