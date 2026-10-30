// Waiting for the server to come back after it restarts (self-update), shared by the
// update panel, which starts it, and App, which shows the overlay while it runs.
import { ref } from 'vue';

import { getHealth, type Health } from './api/client';

// restarting is set while the page waits for the server; App covers the UI with a spinner.
// slow offers a reload once it takes long.
export const restarting = ref<{ title: string; detail: string; slow?: boolean } | null>(null);

export const POLL_MS = 2000;
// A probe stuck on a server that is going away counts as down after this.
const PROBE_TIMEOUT_MS = 5000;
// Without a failed probe for this long, the restart was faster than the polling.
const MISSED_DOWN_MS = 60_000;
// After this long, the overlay says it takes longer; polling goes on.
const SLOW_MS = 180_000;

// page loads the page again; an object so tests can replace it (jsdom can't reload).
export const page = {
  reload: () => window.location.reload(),
  load: (path: string) => window.location.assign(path),
};

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function probe(): Promise<Health | null> {
  try {
    return await getHealth({ signal: AbortSignal.timeout(PROBE_TIMEOUT_MS) });
  } catch {
    return null;
  }
}

// waitForServerRestart waits until the server running fromVersion stopped answering and a
// server answers again on two probes in a row, and returns its health. That is the new
// one, or the old one again when the update rolled back. A different version answering
// counts as restarted even if no probe saw it down (not when fromVersion is unknown).
export async function waitForServerRestart(fromVersion: string): Promise<Health> {
  const waiting = {
    title: 'Updating nudge…',
    detail: 'It restarts on the new image. This page reconnects when it is back.',
  };
  restarting.value = waiting;
  try {
    const start = Date.now();
    let down = false;
    let last: Health | null = null;
    for (;;) {
      await sleep(POLL_MS);
      const elapsed = Date.now() - start;
      if (elapsed >= SLOW_MS && !last) {
        restarting.value = {
          title: 'Still waiting for nudge…',
          detail:
            'It takes longer than usual. If the new image doesn’t start, the old one comes back; this page keeps trying.',
          slow: true,
        };
      }
      const health = await probe();
      if (!health) {
        down = true;
        if (last) restarting.value = waiting;
        last = null;
        continue;
      }
      const restarted =
        down || (fromVersion !== '' && health.version !== fromVersion) || elapsed >= MISSED_DOWN_MS;
      if (!restarted) continue;
      // Twice in a row: the helper may still give up on a new server that crashes.
      if (last) return health;
      last = health;
      restarting.value = { title: 'Reconnecting…', detail: 'nudge is answering again.' };
    }
  } finally {
    restarting.value = null;
  }
}

// waitForServer polls until the server answers, for the "unreachable" screen. It stops
// when signal aborts.
export async function waitForServer(signal: AbortSignal): Promise<boolean> {
  while (!signal.aborted) {
    await sleep(POLL_MS);
    if (signal.aborted) break;
    if ((await probe()) && !signal.aborted) return true;
  }
  return false;
}
