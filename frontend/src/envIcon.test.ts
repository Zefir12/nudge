import { describe, expect, it } from 'vite-plus/test';

import { envIcon, envIcons } from './envIcon';

describe('envIcon', () => {
  it('gives the same name the same icon', () => {
    expect(envIcon('local')).toBe(envIcon('local'));
    expect(envIcons).toContain(envIcon('local'));
    expect(envIcons).toContain(envIcon(''));
  });

  it('spreads names over the set', () => {
    const used = new Set(['local', 'nas', 'prod', 'staging', 'pi', 'vps', 'home', 'lab'].map(envIcon));
    expect(used.size).toBeGreaterThan(3);
  });
});
