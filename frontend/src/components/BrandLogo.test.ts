import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vite-plus/test';

import BrandLogo from './BrandLogo.vue';

describe('BrandLogo', () => {
  it('renders an accessible image with no inline styles or hard-coded colours', () => {
    const svg = mount(BrandLogo).find('svg');
    expect(svg.attributes('role')).toBe('img');
    expect(svg.attributes('aria-label')).toBe('nudge');
    const html = svg.html();
    expect(html).not.toMatch(/style=|stroke="#|fill="#/);
    expect(svg.findAll('.mark, .shadow, .text')).toHaveLength(3);
  });
});
