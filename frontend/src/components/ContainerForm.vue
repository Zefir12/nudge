<script setup lang="ts">
import { computed, reactive, ref } from 'vue';

import type { ContainerSpec } from '../api/client';
import { parsePairs } from '../format';

// ContainerForm edits a ContainerSpec; the view sends it. fields are the API's messages
// per invalid field ("image", "ports.0.host_port").
const props = defineProps<{
  initial?: ContainerSpec;
  fields: Record<string, string>;
  busy: boolean;
  submitLabel: string;
}>();
const emit = defineEmits<{ submit: [spec: ContainerSpec] }>();

interface PortRow {
  host_ip: string;
  host_port: number | '';
  container_port: number | '';
  protocol: string;
}
type MountRow = ContainerSpec['mounts'][number];

const lines = (xs: string[] | undefined) => (xs ?? []).join('\n');
const init = props.initial;
const form = reactive({
  image: init?.image ?? '',
  name: init?.name ?? '',
  command: lines(init?.command),
  env: lines(init?.env),
  ports: (init?.ports ?? []).map((p): PortRow => ({ ...p, host_port: p.host_port || '' })),
  mounts: (init?.mounts ?? []).map((m): MountRow => ({ ...m })),
  networks: (init?.networks ?? []).join(', '),
  restart_policy: init?.restart_policy || 'no',
  labels: Object.entries(init?.labels ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n'),
});
const labelsError = ref('');

const addPort = () => form.ports.push({ host_ip: '', host_port: '', container_port: '', protocol: 'tcp' });
const addMount = () => form.mounts.push({ type: 'volume', source: '', target: '', read_only: false });

// listErrors gathers "env.3"-style messages for a field edited as one line per entry.
function listErrors(prefix: string, what: string) {
  return Object.entries(props.fields)
    .filter(([k]) => k.startsWith(`${prefix}.`))
    .map(([k, msg]) => `${what} ${Number(k.slice(prefix.length + 1)) + 1}: ${msg}`);
}
const envErrors = computed(() => listErrors('env', 'Line'));
const networkErrors = computed(() => listErrors('networks', 'Network'));
const rowErrors = (prefix: string, i: number) =>
  Object.entries(props.fields)
    .filter(([k]) => k.startsWith(`${prefix}.${i}.`))
    .map(([k, msg]) => `${k.split('.')[2]?.replace('_', ' ')}: ${msg}`);

const split = (text: string) =>
  text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean);

function onSubmit() {
  labelsError.value = '';
  let labels: Record<string, string>;
  try {
    labels = parsePairs(form.labels);
  } catch (e) {
    labelsError.value = e instanceof Error ? e.message : String(e);
    return;
  }
  emit('submit', {
    image: form.image.trim(),
    name: form.name.trim(),
    command: split(form.command),
    env: split(form.env),
    ports: form.ports.map((p) => ({
      host_ip: p.host_ip.trim(),
      host_port: Number(p.host_port) || 0,
      container_port: Number(p.container_port) || 0,
      protocol: p.protocol,
    })),
    mounts: form.mounts.map((m) => ({ ...m, source: m.source.trim(), target: m.target.trim() })),
    networks: form.networks
      .split(',')
      .map((n) => n.trim())
      .filter(Boolean),
    restart_policy: form.restart_policy,
    labels,
  });
}
</script>

<template>
  <form class="container-form" @submit.prevent="onSubmit">
    <div class="row">
      <label>
        Image
        <input v-model="form.image" name="image" placeholder="nginx:1.29" required :disabled="busy" />
        <span v-if="fields.image" class="error">{{ fields.image }}</span>
      </label>
      <label>
        Name
        <input v-model="form.name" name="name" placeholder="generated if empty" :disabled="busy" />
        <span v-if="fields.name" class="error">{{ fields.name }}</span>
      </label>
    </div>

    <div class="row">
      <label>
        Command (one argument per line; empty uses the image's)
        <textarea v-model="form.command" name="command" rows="3" :disabled="busy"></textarea>
      </label>
      <label>
        Environment (one NAME=value per line)
        <textarea v-model="form.env" name="env" rows="3" :disabled="busy"></textarea>
        <span v-for="e in envErrors" :key="e" class="error">{{ e }}</span>
      </label>
    </div>

    <fieldset>
      <legend>Ports</legend>
      <div v-for="(p, i) in form.ports" :key="i" class="entry">
        <input
          v-model="p.host_ip"
          :name="`ports.${i}.host_ip`"
          placeholder="host IP (all)"
          aria-label="Host IP"
          :disabled="busy"
        />
        <input
          v-model.number="p.host_port"
          :name="`ports.${i}.host_port`"
          type="number"
          min="0"
          max="65535"
          placeholder="host port (any)"
          aria-label="Host port"
          :disabled="busy"
        />
        <span aria-hidden="true">→</span>
        <input
          v-model.number="p.container_port"
          :name="`ports.${i}.container_port`"
          type="number"
          min="1"
          max="65535"
          placeholder="container port"
          aria-label="Container port"
          required
          :disabled="busy"
        />
        <select v-model="p.protocol" :name="`ports.${i}.protocol`" aria-label="Protocol" :disabled="busy">
          <option value="tcp">tcp</option>
          <option value="udp">udp</option>
          <option value="sctp">sctp</option>
        </select>
        <button type="button" :disabled="busy" @click="form.ports.splice(i, 1)">Remove</button>
        <span v-for="e in rowErrors('ports', i)" :key="e" class="error">{{ e }}</span>
      </div>
      <button type="button" :disabled="busy" @click="addPort">Add port</button>
    </fieldset>

    <fieldset>
      <legend>Volumes and binds</legend>
      <div v-for="(m, i) in form.mounts" :key="i" class="entry">
        <select v-model="m.type" :name="`mounts.${i}.type`" aria-label="Type" :disabled="busy">
          <option value="volume">volume</option>
          <option value="bind">bind</option>
        </select>
        <input
          v-model="m.source"
          :name="`mounts.${i}.source`"
          :placeholder="m.type === 'bind' ? '/path/on/host' : 'volume (new if empty)'"
          aria-label="Source"
          :disabled="busy"
        />
        <span aria-hidden="true">→</span>
        <input
          v-model="m.target"
          :name="`mounts.${i}.target`"
          placeholder="/path/in/container"
          aria-label="Target"
          required
          :disabled="busy"
        />
        <label class="check"
          ><input v-model="m.read_only" type="checkbox" :disabled="busy" /> read-only</label
        >
        <button type="button" :disabled="busy" @click="form.mounts.splice(i, 1)">Remove</button>
        <span v-for="e in rowErrors('mounts', i)" :key="e" class="error">{{ e }}</span>
      </div>
      <button type="button" :disabled="busy" @click="addMount">Add volume or bind</button>
    </fieldset>

    <div class="row">
      <label>
        Networks (comma-separated; empty uses the default)
        <input v-model="form.networks" name="networks" placeholder="bridge" :disabled="busy" />
        <span v-for="e in networkErrors" :key="e" class="error">{{ e }}</span>
      </label>
      <label>
        Restart policy
        <select v-model="form.restart_policy" name="restart_policy" :disabled="busy">
          <option value="no">no</option>
          <option value="always">always</option>
          <option value="unless-stopped">unless-stopped</option>
          <option value="on-failure">on-failure</option>
        </select>
        <span v-if="fields.restart_policy" class="error">{{ fields.restart_policy }}</span>
      </label>
    </div>

    <label>
      Labels (one key=value per line)
      <textarea v-model="form.labels" name="labels" rows="3" :disabled="busy"></textarea>
      <span v-if="labelsError || fields.labels" class="error">{{ labelsError || fields.labels }}</span>
    </label>

    <slot />
    <div>
      <button type="submit" class="primary" :disabled="busy">{{ submitLabel }}</button>
    </div>
  </form>
</template>

<style scoped>
.container-form {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
}

label {
  display: flex;
  flex: 1 1 14rem;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

label.check {
  flex: 0 0 auto;
  flex-direction: row;
  align-items: center;
  gap: 0.3rem;
  font-weight: normal;
}

fieldset {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0.5rem;
  margin: 0;
  padding: 0.75rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
}

legend {
  padding: 0 0.25rem;
  font-weight: 500;
}

.entry {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  width: 100%;
}

.entry input {
  flex: 1 1 8rem;
  min-width: 0;
}

.entry .error {
  flex-basis: 100%;
}

.error {
  font-weight: normal;
}
</style>
