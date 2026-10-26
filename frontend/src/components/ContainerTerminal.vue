<script setup lang="ts">
import '@xterm/xterm/css/xterm.css';

import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import { computed, nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue';

import { openExec, type ExecSession } from '../api/client';

const props = defineProps<{ env: string; id: string }>();

const cmd = ref('/bin/sh');
// status is idle before the first session, live while one runs, then ended.
const status = ref<'idle' | 'live' | 'ended'>('idle');
const message = ref('');
const failed = ref(false);
const box = useTemplateRef<HTMLElement>('box');

let term: Terminal | null = null;
let fit: FitAddon | null = null;
let session: ExecSession | null = null;
let observer: ResizeObserver | null = null;

// connect starts a session in a fresh terminal sized to its box.
async function connect() {
  teardown();
  status.value = 'live';
  message.value = '';
  failed.value = false;
  await nextTick();
  if (!box.value) return;
  const t = new Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, Menlo, Consolas, monospace',
    fontSize: 13,
  });
  const f = new FitAddon();
  t.loadAddon(f);
  t.open(box.value);
  f.fit();
  term = t;
  fit = f;
  const s = openExec(
    props.env,
    props.id,
    { cmd: cmd.value.trim() || undefined, cols: t.cols, rows: t.rows },
    {
      onOutput: (data) => t.write(data),
      onEnd: (e) => {
        status.value = 'ended';
        t.options.disableStdin = true;
        failed.value = !!e.error;
        message.value =
          e.error ?? (e.exitCode === undefined ? 'Session ended' : `Session ended (exit code ${e.exitCode})`);
      },
    },
  );
  session = s;
  t.onData((d) => s.send(d));
  t.onResize(({ cols, rows }) => s.resize(cols, rows));
  if (typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(() => fit?.fit());
    observer.observe(box.value);
  }
  t.focus();
}

function disconnect() {
  session?.close();
  session = null;
  if (term) term.options.disableStdin = true;
  status.value = 'ended';
  failed.value = false;
  message.value = 'Disconnected';
}

function teardown() {
  session?.close();
  session = null;
  observer?.disconnect();
  observer = null;
  term?.dispose();
  term = null;
  fit = null;
}

watch(
  () => [props.env, props.id],
  () => {
    teardown();
    status.value = 'idle';
    message.value = '';
  },
);
onBeforeUnmount(teardown);

const statusText = computed(() => (status.value === 'live' ? 'Connected' : message.value));
</script>

<template>
  <section class="card">
    <h2>Terminal</h2>
    <form class="controls" @submit.prevent="connect">
      <input
        v-model="cmd"
        class="cmd"
        aria-label="Command"
        placeholder="/bin/sh"
        :disabled="status === 'live'"
      />
      <button v-if="status !== 'live'" type="submit">
        {{ status === 'idle' ? 'Connect' : 'Reconnect' }}
      </button>
      <button v-else type="button" @click="disconnect">Disconnect</button>
      <span class="status" :class="{ error: failed }" role="status">{{ statusText }}</span>
    </form>
    <p v-if="status === 'idle'" class="muted">
      Runs the command in the container with a terminal, like <code>docker exec -it</code>.
    </p>
    <div v-else ref="box" class="terminal" aria-label="Terminal"></div>
  </section>
</template>

<style scoped>
.controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
}

.cmd {
  min-width: 12rem;
  font-family: var(--mono);
}

.status {
  color: var(--muted);
}

.status.error {
  color: var(--danger);
}

.terminal {
  height: 24rem;
  padding: 0.25rem;
  border-radius: var(--radius);
  background: #000;
}
</style>
