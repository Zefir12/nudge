<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import {
  getEnvironment,
  getEnvironmentMetrics,
  getEnvironmentSummary,
  listStacks,
  listVolumes,
  type Environment,
} from '../api/client';
import { useLoad } from '../composables/useLoad';
import { currentUser } from '../session';
import ActivityHeatmap from './ActivityHeatmap.vue';
import ConnPlug from './ConnPlug.vue';
import DiskUsage from './DiskUsage.vue';
import EnvironmentSettings from './EnvironmentSettings.vue';
import GearButton from './GearButton.vue';
import SettingsDialog from './SettingsDialog.vue';
import { mockActivity, mockDisk } from './tileMock';
import UsageGauges, { type Gauge } from './UsageGauges.vue';
import UsageHistory from './UsageHistory.vue';
import { formatBytes, formatDate } from '../format';

// EnvironmentTile is one environment on the global dashboard; it opens that environment.
// An online environment's Docker info fills in the counts once it arrives, its usage
// summary the CPU and memory gauges and its stored usage history the 24h charts. Disk and
// activity are mock data for now (tileMock.ts says what each needs). An admin's gear opens
// the environment's settings; changed fires after one of them ran, so the dashboard
// reloads the list.
const props = defineProps<{ env: Environment }>();
const emit = defineEmits<{ changed: [] }>();
const router = useRouter();
const settingsOpen = ref(false);

// refreshMs is how often the gauges and charts are asked for again; the server collects
// every minute by default, so asking more often would repeat the same numbers.
const refreshMs = 60_000;
// The charts: the last 24 hours in 15 minute buckets.
const historyRange = { since: '24h', resolution: '15m' };

const id = computed(() => (props.env.online ? String(props.env.id) : ''));
const { data, error } = useLoad(id, getEnvironment);
const docker = computed(() => data.value?.docker);
const { data: usage, reload: reloadUsage } = useLoad(id, getEnvironmentSummary);
const { data: metrics, reload: reloadMetrics } = useLoad(id, (env) =>
  getEnvironmentMetrics(env, historyRange),
);
const { data: volumes } = useLoad(id, listVolumes);
// Stacks are for admins; other users would only get a 403.
const { data: stacks } = useLoad(
  computed(() => (currentUser.value?.admin ? id.value : '')),
  listStacks,
);

let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  timer = setInterval(() => {
    if (!id.value || document.visibilityState === 'hidden') return;
    void reloadUsage();
    void reloadMetrics();
  }, refreshMs);
});
onUnmounted(() => clearInterval(timer));

// load is the usage against the host: CPU against every CPU, memory against all of it.
const load = computed(() => {
  const d = docker.value;
  const u = usage.value;
  if (!d || !u || !d.ncpu || !d.mem_total) return null;
  return {
    cpu: Math.min(100, u.cpu_percent / d.ncpu),
    mem: Math.min(100, (u.memory_usage / d.mem_total) * 100),
    memLabel: `${formatBytes(u.memory_usage)} / ${formatBytes(d.mem_total)}`,
  };
});

// history is the stored usage as the gauges show it: CPU against every CPU, memory
// against the host's.
const history = computed(() => {
  const d = docker.value;
  const points = metrics.value?.points ?? [];
  if (!d || !d.ncpu || !d.mem_total) return { cpu: [], mem: [] };
  return {
    cpu: points.map((p) => Math.min(100, p.cpu_percent / d.ncpu)),
    mem: points.map((p) => Math.min(100, (p.memory_usage / d.mem_total) * 100)),
  };
});

const diskUsed = mockDisk.parts.reduce((s, p) => s + p.bytes, 0);
const gauges = computed<Gauge[]>(() => [
  {
    label: 'CPU',
    percent: load.value?.cpu ?? null,
    detail: docker.value ? `${docker.value.ncpu} cores` : '',
    tone: 'cpu',
  },
  { label: 'Memory', percent: load.value?.mem ?? null, detail: load.value?.memLabel ?? '', tone: 'mem' },
  // TODO: mock until the disk usage endpoint exists (see tileMock.ts).
  {
    label: 'Disk',
    percent: (diskUsed / mockDisk.total) * 100,
    detail: `${formatBytes(diskUsed)} / ${formatBytes(mockDisk.total)}`,
    tone: 'disk',
    mock: true,
  },
]);

// The heatmap's rows end today.
const days = Array.from({ length: 7 }, (_, i) =>
  new Date(Date.now() - (6 - i) * 86_400_000).toLocaleDateString('en', { weekday: 'short' }),
);

const waiting = computed(() => props.env.kind === 'agent' && !props.env.agent?.enrolled);
const address = computed(() =>
  props.env.kind === 'local'
    ? 'Docker socket'
    : waiting.value
      ? 'agent not enrolled'
      : `agent ${props.env.agent?.version ?? ''}`,
);
// The agent's address (admins only) and last ping, while its tunnel is up.
const link = computed(() => {
  const a = props.env.agent;
  return props.env.online && a && (a.address || a.rtt_ms !== undefined) ? a : null;
});
const pingTone = computed(() => {
  const ms = link.value?.rtt_ms ?? 0;
  return ms < 50 ? 'good' : ms < 150 ? 'warn' : 'bad';
});

const status = computed(() => {
  if (props.env.online) return { label: 'Online', tone: 'good' };
  if (waiting.value) return { label: 'Waiting for agent', tone: 'warn' };
  return { label: 'Offline', tone: 'off' };
});

const to = computed(() => ({ name: 'env', params: { env: String(props.env.id) } }));

// The whole tile opens the environment; the name is its link for the keyboard and for
// opening in a new tab. The tile's own buttons, links and settings dialog do their own
// thing, and a click with a modifier or one that ends a text selection does nothing.
function open(e: MouseEvent) {
  if ((e.target as Element).closest('a, button, dialog')) return;
  if (e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
  if (window.getSelection()?.toString()) return;
  void router.push(to.value);
}
</script>

<template>
  <article class="tile" @click="open">
    <div class="head">
      <ConnPlug :online="env.online" :waiting="waiting" />
      <div class="title">
        <h2>
          <RouterLink :to="to">{{ env.name }}</RouterLink>
        </h2>
        <span class="address">
          {{ address }}
          <span v-if="link" class="link">
            <template v-if="link.address">· {{ link.address }}</template>
            <template v-if="link.rtt_ms !== undefined">
              · <span class="ping" :class="pingTone" title="last ping">{{ link.rtt_ms }} ms</span>
            </template>
          </span>
        </span>
      </div>
      <span class="chip">{{ env.kind === 'local' ? 'socket' : 'agent' }}</span>
      <GearButton
        v-if="currentUser?.admin"
        label="Environment settings"
        class="gear"
        @click="settingsOpen = true"
      />
    </div>
    <SettingsDialog v-if="settingsOpen" :title="`Environment ${env.name}`" @close="settingsOpen = false">
      <EnvironmentSettings :env="env" @changed="emit('changed')" />
    </SettingsDialog>

    <template v-if="env.online">
      <div v-if="docker" class="body">
        <UsageGauges :gauges="gauges" />
        <div class="col">
          <dl class="stats">
            <div>
              <dt>Running</dt>
              <dd>
                {{ docker.containers_running }}<span class="of">/{{ docker.containers }}</span>
              </dd>
            </div>
            <div>
              <dt>Images</dt>
              <dd>{{ docker.images }}</dd>
            </div>
            <div>
              <dt>Volumes</dt>
              <dd>{{ volumes?.length ?? '–' }}</dd>
            </div>
            <div>
              <dt>Stacks</dt>
              <dd>{{ stacks?.length ?? '–' }}</dd>
            </div>
          </dl>
          <UsageHistory
            :cpu="history.cpu"
            :mem="history.mem"
            :cpu-now="load?.cpu ?? null"
            :mem-now="load?.mem ?? null"
          />
          <div class="mock" title="TODO: mock data until there is a disk usage endpoint">
            <DiskUsage :total="mockDisk.total" :parts="mockDisk.parts" :reclaimable="mockDisk.reclaimable" />
          </div>
          <div class="mock" title="TODO: mock data until the server counts events per hour">
            <ActivityHeatmap :counts="mockActivity" :days="days" />
          </div>
        </div>
      </div>
      <p v-else-if="error" class="note error">Could not read the Docker host: {{ error }}</p>
      <p v-else class="note muted">Loading…</p>
    </template>
    <div v-else class="away">
      <template v-if="waiting">
        <strong>Waiting for the agent</strong>
        <span class="muted"
          >Start the agent on the host with the join command from this environment's settings.</span
        >
      </template>
      <template v-else>
        <strong>{{ env.kind === 'agent' ? 'Agent not connected' : 'Docker is not answering' }}</strong>
        <span class="muted">
          <template v-if="env.last_seen_at">Last seen {{ formatDate(env.last_seen_at) }}.</template>
          {{
            env.kind === 'agent'
              ? 'Check that the nudge agent is running on this host.'
              : 'Check the Docker socket.'
          }}
        </span>
      </template>
    </div>

    <div class="foot">
      <span class="engine">
        <template v-if="docker"
          >Docker {{ docker.version }} · {{ docker.os_type }}/{{ docker.architecture }}</template
        >
      </span>
      <span class="status" :class="status.tone">{{ status.label }}</span>
    </div>
  </article>
</template>

<style scoped>
.tile {
  display: flex;
  flex-direction: column;
  gap: 1.4rem;
  min-height: 260px;
  padding: 1.4rem 1.5rem;
  color: var(--text);
  cursor: pointer;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow);
  container-type: inline-size;
  transition:
    border-color 0.15s ease,
    transform 0.15s ease,
    box-shadow 0.15s ease;
}

.tile:hover {
  transition-duration: var(--hover-in);
  border-color: var(--accent-border);
  box-shadow:
    0 10px 32px rgb(0 0 0 / 50%),
    0 0 0 1px var(--accent-soft);
  transform: translateY(-2px);
}

.head {
  display: flex;
  align-items: center;
  gap: 0.8rem;
}

.head .plug {
  width: 22px;
  height: 22px;
}

.title {
  display: flex;
  flex-direction: column;
  flex-grow: 1;
  min-width: 0;
}

h2 {
  margin: 0;
  overflow: hidden;
  font-size: 1.4rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

h2 a {
  color: var(--text);
  text-decoration: none;
}

.head .gear {
  margin: -0.35rem -0.6rem -0.35rem -0.3rem;
}

/* The settings dialog sits in the tile's DOM, but clicks in it don't open the tile. */
.tile dialog {
  cursor: auto;
}

.address,
.engine {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--muted);
}

/* Gauges in a rail on the left, everything else beside them; stacked when narrow. */
.body {
  display: grid;
  grid-template-columns: 8.5rem minmax(0, 1fr);
  gap: 1.25rem;
  align-items: start;
}

@container (max-width: 520px) {
  .body {
    grid-template-columns: minmax(0, 1fr);
  }
}

.col {
  display: flex;
  flex-direction: column;
  gap: 1.1rem;
  min-width: 0;
}

.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0.6rem;
  margin: 0;
}

dt {
  font-size: 10.5px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.1em;
}

dd {
  margin: 0;
  font-family: var(--font-display);
  font-size: 22px;
  font-weight: 600;
  white-space: nowrap;
}

.of {
  font-size: 0.7em;
  color: var(--muted);
}

.ping.good {
  color: var(--ok-text);
}

.ping.warn {
  color: var(--warn);
}

.ping.bad {
  color: var(--danger-text);
}

/* TODO: remove with tileMock.ts; marks the parts that show mock data. */
.mock {
  position: relative;
}

.mock::after {
  padding: 0 0.35rem;
  font-family: var(--font);
  font-size: 9.5px;
  font-weight: 600;
  color: var(--warn);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  background: var(--warn-soft);
  border-radius: var(--radius-sm);
}

.mock::after {
  position: absolute;
  top: -0.45rem;
  right: 0;
  content: 'mock';
}

.note {
  margin: 0;
}

.away {
  display: flex;
  flex-direction: column;
  justify-content: center;
  flex-grow: 1;
  gap: 0.35rem;
  padding: 1.1rem 1.25rem;
  font-size: 13px;
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius);
}

.away strong {
  font-size: 14px;
}

.foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  margin-top: auto;
  padding-top: 1rem;
  border-top: 1px solid var(--border);
}

.status {
  padding: 0.1rem 0.5rem;
  font-size: 12px;
  font-weight: 500;
  white-space: nowrap;
  border-radius: var(--radius-sm);
}

.status.good {
  color: var(--ok-text);
  background: var(--ok-soft);
}

.status.warn {
  color: var(--warn);
  background: var(--warn-soft);
}

.status.off {
  color: var(--muted);
  background: rgb(255 255 255 / 5%);
}

@container (max-width: 420px) {
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
