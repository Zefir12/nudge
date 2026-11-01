import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { POLL_MS, restarting, waitForServer, waitForServerRestart } from './reconnect';
import { stubApi } from './test-utils';

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

// health replies with the version of each poll in turn, null for "down"; the last repeats.
function health(versions: (string | null)[]) {
  let i = 0;
  return stubApi({
    'GET /health': () => {
      const v = versions[Math.min(i++, versions.length - 1)];
      return v === null ? { status: 502, raw: 'Bad Gateway' } : { body: { status: 'ok', version: v } };
    },
  });
}

describe('waitForServerRestart', () => {
  it.each([
    { name: 'a new version after it was down', versions: ['v1', null, null, 'v2'], want: 'v2', polls: 5 },
    { name: 'the old version after a rollback', versions: ['v1', null, 'v1'], want: 'v1', polls: 4 },
    { name: 'a new version it never saw down', versions: ['v2'], want: 'v2', polls: 2 },
    {
      name: 'a server that answered again only once',
      versions: [null, 'v2', null, 'v2'],
      want: 'v2',
      polls: 5,
    },
  ])('returns $name', async ({ versions, want, polls }) => {
    const fetch = health(versions);
    const done = waitForServerRestart('v1');
    expect(restarting.value?.title).toBe('Updating nudge…');
    await vi.advanceTimersByTimeAsync(POLL_MS * polls);
    await expect(done).resolves.toMatchObject({ version: want });
    expect(fetch).toHaveBeenCalledTimes(polls);
    expect(restarting.value).toBeNull();
  });

  it('keeps waiting while the old server answers, then gives up on seeing it down', async () => {
    health(['v1']);
    let result = '';
    void waitForServerRestart('v1').then((h) => (result = h.version));
    await vi.advanceTimersByTimeAsync(50_000);
    expect(result).toBe('');
    expect(restarting.value).not.toBeNull();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(result).toBe('v1');
  });

  it('says so when it takes long', async () => {
    health([null]);
    void waitForServerRestart('v1');
    await vi.advanceTimersByTimeAsync(180_000);
    expect(restarting.value).toMatchObject({ title: 'Still waiting for nudge…', slow: true });
  });

  it('does not take any version for new when the old one is unknown', async () => {
    health(['v1']);
    let result = '';
    void waitForServerRestart('').then((h) => (result = h.version));
    await vi.advanceTimersByTimeAsync(POLL_MS * 5);
    expect(result).toBe('');
  });
});

describe('waitForServer', () => {
  it('resolves once the server answers', async () => {
    health([null, null, 'v1']);
    const done = waitForServer(new AbortController().signal);
    await vi.advanceTimersByTimeAsync(POLL_MS * 3);
    await expect(done).resolves.toBe(true);
  });

  it('stops when aborted', async () => {
    const fetch = health([null]);
    const ctl = new AbortController();
    const done = waitForServer(ctl.signal);
    await vi.advanceTimersByTimeAsync(POLL_MS);
    ctl.abort();
    await vi.advanceTimersByTimeAsync(POLL_MS * 3);
    await expect(done).resolves.toBe(false);
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
