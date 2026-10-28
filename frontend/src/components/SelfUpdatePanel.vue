<script setup lang="ts">
import { onMounted, ref } from 'vue';

import {
  checkAgentUpdate,
  checkSystemUpdate,
  getAgentUpdate,
  getHealth,
  getSystemUpdate,
  startAgentUpdate,
  startSystemUpdate,
  type SelfUpdate,
} from '../api/client';
import { formatDate } from '../format';

// SelfUpdatePanel shows the version of nudge's own container and updates it: the
// server's, or with env the agent's of that environment.
const props = defineProps<{ env?: string }>();

const state = ref<SelfUpdate | null>(null);
const error = ref('');
const note = ref('');
const busy = ref<'' | 'load' | 'check' | 'update' | 'restart'>('load');

const what = () => (props.env ? 'the agent' : 'nudge');
const get = () => (props.env ? getAgentUpdate(props.env) : getSystemUpdate());
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function run(kind: typeof busy.value, fn: () => Promise<void>) {
  busy.value = kind;
  error.value = '';
  try {
    await fn();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = '';
  }
}

const load = () =>
  run('load', async () => {
    state.value = await get();
  });

const check = () =>
  run('check', async () => {
    note.value = '';
    state.value = await (props.env ? checkAgentUpdate(props.env) : checkSystemUpdate());
  });

async function update() {
  if (
    !window.confirm(
      `Update ${what()} now? It restarts on the new image; if that fails, the old one comes back.`,
    )
  ) {
    return;
  }
  note.value = '';
  let started = false;
  await run('update', async () => {
    started = (await (props.env ? startAgentUpdate(props.env) : startSystemUpdate())).started;
  });
  if (error.value) return;
  if (!started) {
    note.value = 'The registry had nothing newer.';
    await load();
    return;
  }
  busy.value = 'restart';
  await waitForRestart();
  if (!props.env) {
    // The new server may serve a new UI.
    window.location.reload();
    return;
  }
  await check();
  note.value = 'The agent restarted.';
}

// waitForRestart waits until the old container stopped answering and the new one
// answers, or gives up after about three minutes (a failed update brings the old one back).
async function waitForRestart() {
  const probe = props.env ? () => getAgentUpdate(props.env as string) : getHealth;
  let down = false;
  for (let i = 0; i < 90; i++) {
    await sleep(2000);
    try {
      await probe();
      if (down || i >= 15) return;
    } catch {
      down = true;
    }
  }
}

onMounted(load);

const statusText: Record<SelfUpdate['status'], string> = {
  available: 'Update available',
  current: 'Up to date',
  unknown: 'Unknown',
};
</script>

<template>
  <section class="self-update">
    <h3 class="eyebrow">Version</h3>
    <dl v-if="state">
      <dt>Running</dt>
      <dd class="mono">{{ state.version || '—' }}</dd>
      <template v-if="state.image">
        <dt>Image</dt>
        <dd class="mono break">{{ state.image }}</dd>
      </template>
      <dt>Status</dt>
      <dd>
        <span class="status" :class="state.status">{{ statusText[state.status] }}</span>
        <span v-if="state.error" class="muted"> · {{ state.error }}</span>
      </dd>
      <template v-if="state.checked_at">
        <dt>Checked</dt>
        <dd>{{ formatDate(state.checked_at) }}</dd>
      </template>
    </dl>
    <p v-if="state && !state.can_update && state.reason" class="muted reason">{{ state.reason }}</p>
    <p v-if="busy === 'restart'" class="restarting" role="status">
      Updating {{ what() }}… it restarts on the new image{{
        env ? '; this panel refreshes when it is back.' : ' and this page reloads when it is back.'
      }}
    </p>
    <p v-if="note" class="muted" role="status">{{ note }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="actions">
      <button type="button" :disabled="busy !== ''" @click="check">
        {{ busy === 'check' ? 'Checking…' : 'Check for updates' }}
      </button>
      <button
        v-if="state?.can_update && state.status === 'available'"
        type="button"
        class="primary"
        :disabled="busy !== ''"
        @click="update"
      >
        {{ busy === 'update' ? 'Pulling…' : 'Update' }}
      </button>
    </div>
  </section>
</template>

<style scoped>
h3 {
  margin: 0 0 0.5rem;
}

dl {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 0.3rem 1rem;
  margin: 0;
}

dt {
  color: var(--muted);
}

dd {
  margin: 0;
}

.mono {
  font-family: var(--mono);
  font-size: 12.5px;
}

.status.available {
  color: var(--accent);
}

.status.current {
  color: var(--ok-text);
}

.reason,
.restarting {
  margin: 0.75rem 0 0;
}

.actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 1rem;
}
</style>
