import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import ConnPlug from './ConnPlug.vue';

describe('ConnPlug', () => {
  it.each([
    { name: 'online', props: { online: true, waiting: false }, on: true, warn: false },
    { name: 'offline', props: { online: false, waiting: false }, on: false, warn: false },
    { name: 'waiting for its agent', props: { online: false, waiting: true }, on: false, warn: true },
    { name: 'online wins over waiting', props: { online: true, waiting: true }, on: true, warn: false },
  ])('$name', ({ props, on, warn }) => {
    const classes = mount(ConnPlug, { props }).find('.plug').classes();
    expect(classes.includes('on')).toBe(on);
    expect(classes.includes('warn')).toBe(warn);
  });
});
