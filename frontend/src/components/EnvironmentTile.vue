<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink } from 'vue-router';

import { getEnvironment, type Environment } from '../api/client';
import { useLoad } from '../composables/useLoad';
import { formatBytes, formatDate } from '../format';

// EnvironmentTile is one environment on the global dashboard; it opens that environment.
// An online environment's Docker info fills in the counts once it arrives.
const props = defineProps<{ env: Environment }>();

const id = computed(() => (props.env.online ? String(props.env.id) : ''));
const { data, error } = useLoad(id, getEnvironment);
const docker = computed(() => data.value?.docker);

const waiting = computed(() => props.env.kind === 'agent' && !props.env.agent?.enrolled);
const address = computed(() =>
  props.env.kind === 'local'
    ? 'Docker socket'
    : waiting.value
      ? 'agent not enrolled'
      : `agent ${props.env.agent?.version ?? ''}`,
);
const status = computed(() => {
  if (props.env.online) return { label: 'Online', tone: 'good' };
  if (waiting.value) return { label: 'Waiting for agent', tone: 'warn' };
  return { label: 'Offline', tone: 'off' };
});
</script>

<template>
  <RouterLink class="tile" :to="{ name: 'env', params: { env: String(env.id) } }">
    <div class="head">
      <span class="lamp" :class="{ on: env.online, warn: !env.online && waiting }" aria-hidden="true" />
      <div class="title">
        <h2>{{ env.name }}</h2>
        <span class="address">{{ address }}</span>
      </div>
      <span class="chip">{{ env.kind === 'local' ? 'socket' : 'agent' }}</span>
    </div>

    <template v-if="env.online">
      <dl v-if="docker" class="stats">
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
          <dt>CPUs</dt>
          <dd>{{ docker.ncpu }}</dd>
        </div>
        <div>
          <dt>Memory</dt>
          <dd>{{ formatBytes(docker.mem_total) }}</dd>
        </div>
      </dl>
      <p v-else-if="error" class="note error">Could not read the Docker host: {{ error }}</p>
      <p v-else class="note muted">Loading…</p>
    </template>
    <div v-else class="away">
      <template v-if="waiting">
        <strong>Waiting for the agent</strong>
        <span class="muted">Start the agent on the host with the join command from Environments.</span>
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
  </RouterLink>
</template>

<style scoped>
.tile {
  display: flex;
  flex-direction: column;
  gap: 1.4rem;
  min-height: 260px;
  padding: 1.5rem;
  color: var(--text);
  text-decoration: none;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow);
  transition:
    border-color 0.15s ease,
    transform 0.15s ease,
    box-shadow 0.15s ease;
}

.tile:hover {
  color: var(--text);
  border-color: var(--accent-border);
  box-shadow:
    0 10px 32px rgb(0 0 0 / 50%),
    0 0 0 1px var(--accent-soft);
  transform: translateY(-2px);
}

.head {
  display: flex;
  align-items: flex-start;
  gap: 0.9rem;
}

.head .lamp {
  width: 10px;
  height: 10px;
  margin-top: 0.65rem;
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
  font-size: 1.5rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.address,
.engine {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--muted);
}

.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0.75rem;
  margin: 0;
}

.stats div {
  display: flex;
  flex-direction: column-reverse;
}

dt {
  font-size: 12px;
  color: var(--muted);
}

dd {
  margin: 0;
  font-family: var(--font-display);
  font-size: 1.6rem;
  font-weight: 600;
  line-height: 1.15;
  white-space: nowrap;
}

.of {
  font-size: 1rem;
  color: var(--muted);
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

@media (max-width: 480px) {
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
