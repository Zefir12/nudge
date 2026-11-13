<script setup lang="ts">
import { onMounted, reactive, ref, shallowRef } from 'vue';

import type { StackEnvExample } from '../api/client';
import {
  type EnvForm,
  type EnvVar,
  commentText,
  formFromEnv,
  formFromExample,
  isSecretName,
} from '../envfile';

// EnvFormEditor edits a .env as a form: one field per variable with its comments above
// it. With loadExample (a git stack's env file) the fields follow the example, and the
// .env's own variables follow under "Other variables"; without it, the .env's variables
// in place. apply hands back the rebuilt text; nothing is saved here.
const props = defineProps<{ env: string; loadExample?: () => Promise<StackEnvExample> }>();
const emit = defineEmits<{ apply: [env: string]; cancel: [] }>();

const loading = ref(false);
const loadError = ref('');
const example = shallowRef<StackEnvExample | null>(null);
const form = shallowRef<EnvForm | null>(null);
const fields = ref<EnvVar[]>([]);
const others = ref<EnvVar[]>([]);
const dropped = ref<string[]>([]);
// shown holds the field keys whose secret value is shown.
const shown = reactive(new Set<string>());

function use(f: EnvForm) {
  form.value = f;
  // The form's own objects, made reactive, so build() sees what was typed.
  fields.value = f.fields;
  others.value = f.others;
  dropped.value = f.dropped;
}

async function load() {
  loadError.value = '';
  if (!props.loadExample) {
    use(formFromEnv(props.env));
    return;
  }
  loading.value = true;
  try {
    const ex = await props.loadExample();
    example.value = ex;
    use(formFromExample(props.env, ex.content));
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}

// withoutExample edits the .env alone when the example can't be read.
function withoutExample() {
  loadError.value = '';
  use(formFromEnv(props.env));
}

onMounted(load);

const masked = (key: string, v: EnvVar) => isSecretName(v.name) && !shown.has(key);
const toggle = (key: string) => (shown.has(key) ? shown.delete(key) : shown.add(key));
const multiline = (v: EnvVar) => v.value.includes('\n');
const shortCommit = (c: string) => c.slice(0, 12);

function apply() {
  if (form.value) emit('apply', form.value.build());
}
</script>

<template>
  <form class="env-form" @submit.prevent="apply">
    <p v-if="loading" class="muted" role="status">Reading the env file from the repository…</p>
    <template v-else-if="loadError">
      <p class="error" role="alert">The repository's env file can't be read: {{ loadError }}</p>
      <div class="actions">
        <button type="button" @click="load">Retry</button>
        <button type="button" @click="withoutExample">Edit the .env alone</button>
      </div>
    </template>
    <template v-else-if="form">
      <p v-if="example" class="muted">
        Variables of <code>{{ example.path }}</code> at <code>{{ shortCommit(example.commit) }}</code
        >, valued from the .env or else from the example.
      </p>
      <p v-if="dropped.length" class="warning" role="note">
        The .env sets {{ dropped.join(', ') }} more than once. The form uses the last line for each; Apply
        leaves the earlier ones out.
      </p>
      <p v-if="!fields.length && !others.length" class="muted">The .env has no variables.</p>
      <template v-for="(section, s) in [fields, others]" :key="s">
        <h3 v-if="s === 1 && others.length">Other variables</h3>
        <p v-if="s === 1 && others.length" class="muted">
          In the .env but not in the example; written after it.
        </p>
        <div v-for="(v, i) in section" :key="`${s}-${i}`" class="var">
          <label :for="`env-${s}-${i}`">
            <span v-for="(c, j) in v.comments" :key="j" class="comment">{{ commentText(c) }}</span>
            <code class="name">{{ v.name }}</code>
          </label>
          <div class="value">
            <textarea
              v-if="multiline(v)"
              :id="`env-${s}-${i}`"
              v-model="v.value"
              :rows="Math.min(v.value.split('\n').length, 8)"
              spellcheck="false"
            />
            <input
              v-else
              :id="`env-${s}-${i}`"
              v-model="v.value"
              :type="masked(`${s}-${i}`, v) ? 'password' : 'text'"
              autocomplete="off"
              spellcheck="false"
            />
            <button
              v-if="isSecretName(v.name) && !multiline(v)"
              type="button"
              class="toggle"
              :aria-label="`${masked(`${s}-${i}`, v) ? 'Show' : 'Hide'} ${v.name}`"
              @click="toggle(`${s}-${i}`)"
            >
              {{ masked(`${s}-${i}`, v) ? 'Show' : 'Hide' }}
            </button>
          </div>
        </div>
      </template>
      <div class="actions">
        <button type="submit" class="primary">Apply</button>
        <button type="button" @click="emit('cancel')">Cancel</button>
        <span class="muted">Apply puts the text in the .env field; save the stack as usual.</span>
      </div>
    </template>
  </form>
</template>

<style scoped>
.env-form {
  display: grid;
  gap: 0.75rem;
}

h3 {
  margin: 0.5rem 0 0;
  font-size: 0.95rem;
}

p {
  margin: 0;
}

.warning {
  padding: 0.4rem 0.6rem;
  border-radius: 4px;
  background: var(--warn-soft);
}

.var {
  display: grid;
  gap: 0.25rem;
}

label {
  display: grid;
  gap: 0.1rem;
}

.comment {
  color: var(--muted);
  font-size: 0.85rem;
  white-space: pre-wrap;
}

.name {
  font-family: var(--mono);
}

.value {
  display: flex;
  gap: 0.4rem;
}

.value input,
.value textarea {
  flex: 1;
  min-width: 0;
  font-family: var(--mono);
}

.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}
</style>
