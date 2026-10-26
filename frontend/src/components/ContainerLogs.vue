<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { streamLogs, type LogLine } from '../api/client';
import { matches } from '../format';

const props = defineProps<{ env: string; id: string; name: string }>();

// maxLines bounds the memory a long followed stream takes; older lines are dropped.
const maxLines = 10000;

const tail = ref<'100' | '500' | '1000' | 'all'>('500');
const timestamps = ref(false);
const search = ref('');
const paused = ref(false);
const status = ref<'live' | 'ended' | 'error'>('live');
const error = ref('');
// Each line has an id to key on; lines arriving within a frame are added together, so a
// chatty container re-renders once per frame, not once per line.
type Shown = LogLine & { n: number };
const lines = shallowRef<Shown[]>([]);
const dropped = ref(false);
const box = ref<HTMLElement | null>(null);

let close: (() => void) | null = null;
let generation = 0;
let next = 0;
let pending: Shown[] = [];
let frame = 0;

function flush() {
  frame = 0;
  const all = lines.value.concat(pending);
  pending = [];
  if (all.length > maxLines) {
    all.splice(0, all.length - maxLines);
    dropped.value = true;
  }
  lines.value = all;
}

function open() {
  close?.();
  cancelAnimationFrame(frame);
  frame = 0;
  pending = [];
  lines.value = [];
  dropped.value = false;
  status.value = 'live';
  error.value = '';
  // A message the old socket still delivers belongs to the old stream.
  const gen = ++generation;
  close = streamLogs(
    props.env,
    props.id,
    { follow: true, tail: tail.value === 'all' ? 'all' : Number(tail.value), timestamps: timestamps.value },
    {
      onLine: (l) => {
        if (gen !== generation) return;
        pending.push({ ...l, n: next++ });
        if (!frame) frame = requestAnimationFrame(flush);
      },
      onEnd: (err) => {
        if (gen !== generation) return;
        status.value = err ? 'error' : 'ended';
        error.value = err ?? '';
      },
    },
  );
}

watch(() => [props.env, props.id, tail.value, timestamps.value], open, { immediate: true });
onBeforeUnmount(() => {
  generation++;
  cancelAnimationFrame(frame);
  close?.();
});

const shown = computed(() => lines.value.filter((l) => matches(search.value, l.line)));

// Follow new lines unless paused; scrolling up pauses, scrolling back down resumes.
watch(shown, async () => {
  if (paused.value) return;
  await nextTick();
  if (box.value) box.value.scrollTop = box.value.scrollHeight;
});

function onScroll() {
  const el = box.value;
  if (!el) return;
  paused.value = el.scrollTop + el.clientHeight < el.scrollHeight - 8;
}

async function togglePause() {
  paused.value = !paused.value;
  if (paused.value) return;
  await nextTick();
  if (box.value) box.value.scrollTop = box.value.scrollHeight;
}

function download() {
  const text = lines.value
    .map((l) => (l.time ? `${l.time} ${l.line}` : l.line))
    .join('\n')
    .concat('\n');
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = `${props.name}.log`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

const statusText = computed(() => {
  if (status.value === 'live') return paused.value ? 'Live, paused' : 'Live';
  if (status.value === 'ended') return 'Stream ended';
  return error.value;
});
</script>

<template>
  <section class="card">
    <h2>Logs</h2>
    <div class="controls">
      <label>
        Tail
        <select v-model="tail" aria-label="Lines to show">
          <option value="100">100</option>
          <option value="500">500</option>
          <option value="1000">1000</option>
          <option value="all">all</option>
        </select>
      </label>
      <label><input v-model="timestamps" type="checkbox" /> timestamps</label>
      <input v-model="search" type="search" placeholder="Search" aria-label="Search logs" />
      <button type="button" @click="togglePause">{{ paused ? 'Resume' : 'Pause' }}</button>
      <button type="button" :disabled="lines.length === 0" @click="download">Download</button>
      <button v-if="status !== 'live'" type="button" @click="open">Reconnect</button>
      <span class="status" :class="status" role="status">{{ statusText }}</span>
    </div>
    <p v-if="dropped" class="muted">Showing (and downloading) the last {{ maxLines }} lines.</p>
    <div ref="box" class="logs" tabindex="0" aria-label="Log lines" @scroll="onScroll">
      <p v-if="shown.length === 0" class="muted">{{ search ? 'No matching lines.' : 'No log lines.' }}</p>
      <div v-for="l in shown" :key="l.n" class="line" :class="l.stream">
        <span v-if="l.time" class="time">{{ l.time }}</span
        >{{ l.line }}
      </div>
    </div>
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

.controls label {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.status {
  color: var(--muted);
}

.status.error {
  color: var(--danger);
}

.logs {
  height: 24rem;
  overflow: auto;
  padding: 0.5rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg);
  font-family: var(--mono);
  font-size: 0.85rem;
}

.line {
  white-space: pre-wrap;
  word-break: break-all;
}

.line.stderr {
  color: var(--danger);
}

.time {
  margin-right: 0.5rem;
  color: var(--muted);
}
</style>
