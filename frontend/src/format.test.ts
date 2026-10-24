import { describe, expect, it } from 'vite-plus/test';

import { formatBytes, formatPort, matches, shortId } from './format';

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
});
