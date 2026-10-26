import { describe, expect, it } from 'vite-plus/test';

import { readCompose } from './compose';

describe('readCompose', () => {
  it.each([
    ['services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n', [], ['web', 'db']],
    ['services: {}\n', [], []],
    ['name: x\n', [], []],
    ['services:\n', [], []],
    ['', ['The file is empty.'], []],
    ['# only a comment\n', ['The file is empty.'], []],
    ['- a\n- b\n', ['The file must be a mapping with a services: key.'], []],
    ['services: [web]\n', ['services: must be a mapping of service names.'], []],
  ])('reads %j', (text, errors, services) => {
    expect(readCompose(text)).toEqual({ errors, services });
  });

  it.each([
    ['services:\n  web:\n    image: nginx\n   ports: [80]\n', /line 4/],
    ['services:\n  web: {}\n  web: {}\n', /unique/i],
    ['services: [\n', /flow sequence|line/i],
  ])('reports syntax errors in %j on one line', (text, pattern) => {
    const r = readCompose(text);
    expect(r.errors.length).toBeGreaterThan(0);
    expect(r.errors[0]).toMatch(pattern);
    expect(r.errors[0]).not.toContain('\n');
    expect(r.services).toEqual([]);
  });
});
