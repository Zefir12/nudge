import { describe, expect, it } from 'vite-plus/test';

import { formatBytes, formatPort, matches, parsePairs, shortId, timeKey } from './format';

describe('format', () => {
  it.each([
    [0, '0 B'],
    [999, '999 B'],
    [1500, '1.5 KB'],
    [16e9, '16.0 GB'],
  ])('formatBytes(%d) = %s', (n, want) => {
    expect(formatBytes(n)).toBe(want);
  });

  it('shortId drops the digest prefix', () => {
    expect(shortId('sha256:0123456789abcdef')).toBe('0123456789ab');
    expect(shortId('abc')).toBe('abc');
  });

  it.each([
    [{ private_port: 80, protocol: 'tcp', host_ip: '', public_port: 0 }, '80/tcp'],
    [{ private_port: 80, protocol: 'tcp', host_ip: '', public_port: 8080 }, '8080 → 80/tcp'],
    [{ private_port: 53, protocol: 'udp', host_ip: '::', public_port: 53 }, ':::53 → 53/udp'],
  ])('formatPort %#', (p, want) => {
    expect(formatPort(p)).toBe(want);
  });

  it('matches ignores case and an empty query', () => {
    expect(matches('', 'x')).toBe(true);
    expect(matches(' Web ', 'my-web-1')).toBe(true);
    expect(matches('db', 'web', undefined)).toBe(false);
  });

  it('parsePairs reads key=value lines', () => {
    expect(parsePairs('a=1\n\n  b = x=y \nflag\n')).toEqual({ a: '1', b: 'x=y', flag: '' });
    expect(parsePairs('')).toEqual({});
    expect(() => parsePairs('=oops')).toThrow('"=oops" has no name before "="');
  });
});

describe('timeKey', () => {
  it('sorts Docker timestamps of any precision', () => {
    const times = [
      '2026-10-01T12:00:00.12Z',
      '2026-10-01T12:00:00Z',
      '2026-10-01T12:00:00.1Z',
      '2026-10-01T12:00:00.123456789Z',
      '2026-10-01T11:59:59.9Z',
    ];
    expect([...times].sort((a, b) => timeKey(a).localeCompare(timeKey(b)))).toEqual([
      '2026-10-01T11:59:59.9Z',
      '2026-10-01T12:00:00Z',
      '2026-10-01T12:00:00.1Z',
      '2026-10-01T12:00:00.12Z',
      '2026-10-01T12:00:00.123456789Z',
    ]);
    expect(timeKey(undefined)).toBe('');
  });
});
