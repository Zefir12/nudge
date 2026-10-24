<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { streamLogs, type LogLine } from '../api/client';
import { matches, timeKey } from '../format';

// StackLogs follows the logs of a stack's containers in one view: one log stream per
// container, lines merged by the daemon's timestamp and tagged with the service.
export interface StackLogSource {
  id: string;
  name: string;
  service: string;
}

const props = defineProps<{ env: string; containers: StackLogSource[] }>();

// maxLines bounds the memory long followed streams take; older lines are dropped.
const maxLines = 10000;

const tail = ref<'50' | '200' | '1000'>('200');
const timestamps = ref(false);
const search = ref('');
const paused = ref(false);
type Shown = LogLine & { n: number; key: string; service: string; tone: number };
const lines = shallowRef<Shown[]>([]);
const dropped = ref(false);
const live = ref(0);
const errors = ref<string[]>([]);
const box = ref<HTMLElement | null>(null);

let closers: (() => void)[] = [];
let generation = 0;
let next = 0;
let pending: Shown[] = [];
let frame = 0;

function flush() {
  frame = 0;
  const last = lines.value.at(-1)?.key ?? '';
  const sorted = pending.every((l, i) => l.key >= (i ? pending[i - 1]!.key : last));
  const all = lines.value.concat(pending);
  pending = [];
  // Streams start with their tails in bursts; after that, lines mostly arrive in order.
  if (!sorted) all.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : a.n - b.n));
  if (all.length > maxLines) {
    all.splice(0, all.length - maxLines);
    dropped.value = true;
  }
  lines.value = all;
}

function stop() {
  generation++;
  for (const close of closers) close();
  closers = [];
  cancelAnimationFrame(frame);
  frame = 0;
  pending = [];
}

const services = computed(() => [...new Set(props.containers.map((c) => c.service))].sort());

function open() {
  stop();
  lines.value = [];
  dropped.value = false;
  errors.value = [];
  live.value = props.containers.length;
  const gen = generation;
  for (const c of props.containers) {
    const tone = services.value.indexOf(c.service) % 6;
    closers.push(
      streamLogs(
        props.env,
        c.id,
        { follow: true, tail: Number(tail.value), timestamps: true },
        {
          onLine: (l) => {
            if (gen !== generation) return;
            pending.push({ ...l, n: next++, key: timeKey(l.time), service: c.service, tone });
            // Frames don't run in a background tab; keep pending bounded meanwhile.
            if (pending.length > maxLines) {
              pending.splice(0, pending.length - maxLines);
              dropped.value = true;
            }
            if (!frame) frame = requestAnimationFrame(flush);
          },
          onEnd: (err) => {
            if (gen !== generation) return;
            live.value--;
            if (err) errors.value = [...errors.value, `${c.name}: ${err}`];
          },
        },
      ),
    );
  }
}

// The detail page reloads often; only a different set of containers reconnects.
const ids = computed(() =>
  props.containers
    .map((c) => c.id)
    .sort()
    .join(' '),
);
watch(() => [props.env, ids.value, tail.value], open, { immediate: true });
onBeforeUnmount(stop);

const shown = computed(() => lines.value.filter((l) => matches(search.value, l.line, l.service)));

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

const statusText = computed(() => {
  const n = props.containers.length;
  if (live.value > 0) return `${paused.value ? 'Paused, f' : 'F'}ollowing ${live.value} of ${n} containers`;
  return 'Streams ended';
});
</script>

<template>
  <section class="card">
    <h2>Logs</h2>
    <p v-if="containers.length === 0" class="muted">The stack has no containers.</p>
    <template v-else>
      <div class="controls">
        <label>
          Tail
          <select v-model="tail" aria-label="Lines per container">
            <option value="50">50</option>
            <option value="200">200</option>
            <option value="1000">1000</option>
          </select>
        </label>
        <label><input v-model="timestamps" type="checkbox" /> timestamps</label>
        <input v-model="search" type="search" placeholder="Search" aria-label="Search logs" />
        <button type="button" @click="togglePause">{{ paused ? 'Resume' : 'Pause' }}</button>
        <button v-if="live === 0" type="button" @click="open">Reconnect</button>
        <span class="status" role="status">{{ statusText }}</span>
      </div>
      <ul v-if="errors.length" class="error-list" role="alert">
        <li v-for="e in errors" :key="e">{{ e }}</li>
      </ul>
      <p v-if="dropped" class="muted">Showing the last {{ maxLines }} lines.</p>
      <div ref="box" class="logs" tabindex="0" aria-label="Log lines" @scroll="onScroll">
        <p v-if="shown.length === 0" class="muted">{{ search ? 'No matching lines.' : 'No log lines.' }}</p>
        <div v-for="l in shown" :key="l.n" class="line" :class="l.stream">
          <span class="tag" :class="`tone-${l.tone}`">{{ l.service }}</span
          ><span v-if="timestamps && l.time" class="time">{{ l.time }}</span
          >{{ l.line }}
        </div>
      </div>
    </template>
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

.error-list {
  margin: 0 0 0.5rem;
  padding-left: 1.25rem;
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

.tag {
  display: inline-block;
  min-width: 6ch;
  margin-right: 0.5rem;
  font-weight: 600;
}

.tone-0 {
  color: var(--accent);
}

.tone-1 {
  color: var(--ok);
}

.tone-2 {
  color: var(--warn);
}

.tone-3 {
  color: var(--text);
}

.tone-4 {
  color: var(--muted);
}

.tone-5 {
  color: var(--danger);
}

.time {
  margin-right: 0.5rem;
  color: var(--muted);
}
</style>
