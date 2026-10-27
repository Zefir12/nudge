<script setup lang="ts">
import { computed, reactive } from 'vue';

import type { AllowanceLimits, AllowanceUsage, Network, TenantSpec, Volume } from '../api/client';
import { formatBinaryBytes } from '../format';

// TenantForm edits a user's container (a TenantSpec); the view sends it. The controls
// follow the allowance: CPU and memory sliders stop at what is left (plus what the edited
// container already has, as a recreate leaves it out), host ports stay in the user's
// range, and volumes and networks are picked from the user's own. The API enforces all of
// it; fields are its messages per invalid field ("ports.0.host_port").
const props = defineProps<{
  initial?: TenantSpec;
  limits: AllowanceLimits;
  left: AllowanceUsage;
  volumes: Volume[];
  networks: Network[];
  fields: Record<string, string>;
  busy: boolean;
  submitLabel: string;
}>();
// create holds the indexes of mounts whose volume is new: the view creates them first.
const emit = defineEmits<{ submit: [spec: TenantSpec, create: number[]] }>();

const MiB = 1024 * 1024;
const cpuStep = 50;
const memStep = 8;

interface PortRow {
  host_port: number | '';
  container_port: number | '';
  protocol: string;
}
interface MountRow {
  // volume is an owned volume's name, or '' for a new one named by newName.
  volume: string;
  newName: string;
  target: string;
  read_only: boolean;
}

const init = props.initial;
const maxCpu = computed(() => Math.max(0, props.left.cpus_milli + (init?.cpus_milli ?? 0)));
const maxMem = computed(() =>
  Math.max(0, Math.floor((props.left.memory_bytes + (init?.memory_bytes ?? 0)) / MiB)),
);

const lines = (xs: string[] | undefined) => (xs ?? []).join('\n');
const form = reactive({
  image: init?.image ?? '',
  name: init?.name ?? '',
  command: lines(init?.command),
  env: lines(init?.env),
  cpus: init?.cpus_milli ?? Math.min(500, maxCpu.value),
  memory: init ? Math.round(init.memory_bytes / MiB) : Math.min(256, maxMem.value),
  ports: (init?.ports ?? []).map((p): PortRow => ({ ...p, host_port: p.host_port || '' })),
  mounts: (init?.mounts ?? []).map((m): MountRow => ({ ...m, newName: '' })),
  networks: [...(init?.networks ?? [])],
  restart_policy: init?.restart_policy || 'unless-stopped',
  health: !!init?.healthcheck,
  healthCommand: lines(init?.healthcheck?.command),
  interval: init?.healthcheck?.interval_seconds ?? 30,
  timeout: init?.healthcheck?.timeout_seconds ?? 5,
  startPeriod: init?.healthcheck?.start_period_seconds ?? 0,
  retries: init?.healthcheck?.retries ?? 3,
});

// The image is a select when the allowance lists plain images, else free text (with the
// patterns as suggestions when there are any).
const patterns = computed(() => props.limits.images);
const imageSelect = computed(
  () => patterns.value.length > 0 && patterns.value.every((p) => !/[*?[\\]/.test(p)),
);
const imageOptions = computed(() =>
  init && !patterns.value.includes(init.image) ? [init.image, ...patterns.value] : patterns.value,
);

const hasPorts = computed(() => props.limits.port_min > 0);
const portRange = computed(() => {
  const { port_min: lo, port_max: hi } = props.limits;
  return lo === hi ? `${lo}` : `${lo}–${hi}`;
});

const addPort = () => form.ports.push({ host_port: '', container_port: '', protocol: 'tcp' });
const addMount = () =>
  form.mounts.push({ volume: props.volumes[0]?.name ?? '', newName: '', target: '', read_only: false });

// listErrors gathers "env.3"-style messages for a field edited as one line per entry.
function listErrors(prefix: string, what: string) {
  return Object.entries(props.fields)
    .filter(([k]) => k.startsWith(`${prefix}.`) && /^\d+$/.test(k.slice(prefix.length + 1)))
    .map(([k, msg]) => `${what} ${Number(k.slice(prefix.length + 1)) + 1}: ${msg}`);
}
const envErrors = computed(() => listErrors('env', 'Line'));
const commandErrors = computed(() => listErrors('command', 'Argument'));
const networkErrors = computed(() => listErrors('networks', 'Network'));
const healthErrors = computed(() =>
  Object.entries(props.fields)
    .filter(([k]) => k.startsWith('healthcheck'))
    .map(([k, msg]) => `${k.replace('healthcheck.', '').replaceAll('_', ' ')}: ${msg}`),
);
const rowErrors = (prefix: string, i: number) =>
  Object.entries(props.fields)
    .filter(([k]) => k.startsWith(`${prefix}.${i}.`))
    .map(([k, msg]) => `${k.split('.')[2]?.replaceAll('_', ' ')}: ${msg}`);

const split = (text: string) =>
  text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean);

function onSubmit() {
  const create: number[] = [];
  const mounts = form.mounts.map((m, i) => {
    if (m.volume === '') create.push(i);
    return { volume: m.volume || m.newName.trim(), target: m.target.trim(), read_only: m.read_only };
  });
  emit(
    'submit',
    {
      image: form.image.trim(),
      name: form.name.trim(),
      command: split(form.command),
      entrypoint: init?.entrypoint ?? [],
      env: split(form.env),
      cpus_milli: Number(form.cpus) || 0,
      memory_bytes: (Number(form.memory) || 0) * MiB,
      ports: form.ports.map((p) => ({
        host_port: Number(p.host_port) || 0,
        container_port: Number(p.container_port) || 0,
        protocol: p.protocol,
      })),
      mounts,
      networks: [...form.networks],
      restart_policy: form.restart_policy,
      healthcheck: form.health
        ? {
            command: split(form.healthCommand),
            interval_seconds: Number(form.interval) || 0,
            timeout_seconds: Number(form.timeout) || 0,
            start_period_seconds: Number(form.startPeriod) || 0,
            retries: Number(form.retries) || 0,
          }
        : null,
      labels: init?.labels ?? {},
    },
    create,
  );
}
</script>

<template>
  <form class="tenant-form" @submit.prevent="onSubmit">
    <div class="row">
      <label>
        Image
        <select v-if="imageSelect" v-model="form.image" name="image" required :disabled="busy">
          <option value="" disabled>Choose an image</option>
          <option v-for="p in imageOptions" :key="p" :value="p">{{ p }}</option>
        </select>
        <template v-else>
          <input
            v-model="form.image"
            name="image"
            placeholder="nginx:1.29"
            required
            list="tenant-images"
            :disabled="busy"
          />
          <datalist v-if="patterns.length" id="tenant-images">
            <option v-for="p in patterns" :key="p" :value="p" />
          </datalist>
          <span v-if="patterns.length" class="hint">Allowed: {{ patterns.join(', ') }}</span>
        </template>
        <span v-if="fields.image" class="error">{{ fields.image }}</span>
      </label>
      <label>
        Name
        <input
          v-model="form.name"
          name="name"
          :placeholder="initial ? 'keep the name' : 'web'"
          :required="!initial"
          :disabled="busy"
        />
        <span v-if="fields.name" class="error">{{ fields.name }}</span>
      </label>
    </div>

    <fieldset>
      <legend>Resources</legend>
      <div class="slider">
        <label for="tenant-cpus">CPU</label>
        <input
          id="tenant-cpus"
          v-model.number="form.cpus"
          name="cpus_milli"
          type="range"
          :min="cpuStep"
          :max="maxCpu"
          :step="cpuStep"
          :disabled="busy || maxCpu < cpuStep"
        />
        <span class="val">{{ form.cpus / 1000 }} / {{ maxCpu / 1000 }} CPU</span>
      </div>
      <span v-if="maxCpu < cpuStep" class="error">No CPU left in your allowance.</span>
      <span v-if="fields.cpus_milli" class="error">{{ fields.cpus_milli }}</span>
      <div class="slider">
        <label for="tenant-memory">Memory</label>
        <input
          id="tenant-memory"
          v-model.number="form.memory"
          name="memory_bytes"
          type="range"
          :min="memStep"
          :max="maxMem"
          :step="memStep"
          :disabled="busy || maxMem < memStep"
        />
        <span class="val">{{ form.memory }} MiB / {{ formatBinaryBytes(maxMem * MiB) }}</span>
      </div>
      <span v-if="maxMem < memStep" class="error">No memory left in your allowance.</span>
      <span v-if="fields.memory_bytes" class="error">{{ fields.memory_bytes }}</span>
    </fieldset>

    <fieldset>
      <legend>Ports</legend>
      <p v-if="!hasPorts" class="hint">Your allowance has no host ports.</p>
      <div v-for="(p, i) in form.ports" :key="i" class="entry">
        <input
          v-model.number="p.host_port"
          :name="`ports.${i}.host_port`"
          type="number"
          :min="limits.port_min"
          :max="limits.port_max"
          :placeholder="`host port (any of ${portRange})`"
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
        </select>
        <button type="button" :disabled="busy" @click="form.ports.splice(i, 1)">Remove</button>
        <span v-for="e in rowErrors('ports', i)" :key="e" class="error">{{ e }}</span>
      </div>
      <button v-if="hasPorts" type="button" :disabled="busy" @click="addPort">Add port</button>
    </fieldset>

    <fieldset>
      <legend>Volumes</legend>
      <div v-for="(m, i) in form.mounts" :key="i" class="entry">
        <select v-model="m.volume" :name="`mounts.${i}.volume`" aria-label="Volume" :disabled="busy">
          <option v-for="v in volumes" :key="v.name" :value="v.name">{{ v.name }}</option>
          <option value="">New volume…</option>
        </select>
        <input
          v-if="m.volume === ''"
          v-model="m.newName"
          :name="`mounts.${i}.new`"
          placeholder="new volume name"
          aria-label="New volume name"
          required
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
      <span v-if="fields.mounts" class="error">{{ fields.mounts }}</span>
      <button type="button" :disabled="busy" @click="addMount">Add volume</button>
    </fieldset>

    <fieldset>
      <legend>Networks</legend>
      <p class="hint">None chosen: your default network.</p>
      <label v-for="n in networks" :key="n.id" class="check">
        <input v-model="form.networks" type="checkbox" name="networks" :value="n.name" :disabled="busy" />
        {{ n.name }}
      </label>
      <span v-for="e in networkErrors" :key="e" class="error">{{ e }}</span>
    </fieldset>

    <div class="row">
      <label>
        Command (one argument per line; empty uses the image's)
        <textarea v-model="form.command" name="command" rows="3" :disabled="busy"></textarea>
        <span v-for="e in commandErrors" :key="e" class="error">{{ e }}</span>
      </label>
      <label>
        Environment (one NAME=value per line)
        <textarea v-model="form.env" name="env" rows="3" :disabled="busy"></textarea>
        <span v-for="e in envErrors" :key="e" class="error">{{ e }}</span>
      </label>
    </div>

    <div class="row">
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

    <fieldset>
      <legend>Healthcheck</legend>
      <label class="check">
        <input v-model="form.health" type="checkbox" name="health" :disabled="busy" />
        Own healthcheck (else the image's)
      </label>
      <template v-if="form.health">
        <label class="wide">
          Command (one argument per line, run without a shell)
          <textarea
            v-model="form.healthCommand"
            name="healthcheck.command"
            rows="2"
            placeholder="/bin/check"
            :disabled="busy"
          ></textarea>
        </label>
        <div class="row">
          <label>
            Interval (s)
            <input v-model.number="form.interval" type="number" min="0" max="86400" :disabled="busy" />
          </label>
          <label>
            Timeout (s)
            <input v-model.number="form.timeout" type="number" min="0" max="86400" :disabled="busy" />
          </label>
          <label>
            Start period (s)
            <input v-model.number="form.startPeriod" type="number" min="0" max="86400" :disabled="busy" />
          </label>
          <label>
            Retries
            <input v-model.number="form.retries" type="number" min="0" max="100" :disabled="busy" />
          </label>
        </div>
      </template>
      <span v-for="e in healthErrors" :key="e" class="error">{{ e }}</span>
    </fieldset>

    <slot />
    <div>
      <button type="submit" class="primary" :disabled="busy">{{ submitLabel }}</button>
    </div>
  </form>
</template>

<style scoped>
.tenant-form {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  width: 100%;
}

label {
  display: flex;
  flex: 1 1 14rem;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

label.wide {
  width: 100%;
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

.slider {
  display: grid;
  grid-template-columns: 5rem minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.75rem;
  width: 100%;
}

.slider label {
  flex: none;
}

.val {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--muted);
}

.entry {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  width: 100%;
}

.entry input,
.entry select {
  flex: 1 1 8rem;
  min-width: 0;
}

.entry .error {
  flex-basis: 100%;
}

.hint {
  margin: 0;
  font-size: 12px;
  font-weight: normal;
  color: var(--muted);
}

.error {
  font-weight: normal;
}
</style>
