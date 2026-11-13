<script setup lang="ts">
import { computed, ref, watch } from 'vue';

import { type StackEnvExample, checkStack } from '../api/client';
import { readCompose } from '../compose';
import CodeEditor from './CodeEditor.vue';
import EnvFormEditor from './EnvFormEditor.vue';
import SettingsDialog from './SettingsDialog.vue';

// StackEditor edits a stack's compose.yaml and .env. Syntax errors show while typing;
// compose's own check runs on demand and from validate(), which the views call before
// saving.
const compose = defineModel<string>('compose', { required: true });
const env = defineModel<string>('env', { required: true });
// composeFromGit makes compose.yaml read-only: it comes from a repository.
// loadEnvExample reads the repository's env file, which the .env form editor follows.
defineProps<{
  disabled?: boolean;
  composeFromGit?: boolean;
  loadEnvExample?: () => Promise<StackEnvExample>;
}>();

// envForm opens the .env form editor; it hands back the rebuilt text.
const envForm = ref(false);

function applyEnvForm(text: string) {
  env.value = text;
  envForm.value = false;
}

const syntax = computed(() => readCompose(compose.value));
const checking = ref(false);
// composeErrors are compose's answer for checkedText; editing makes them stale.
const composeErrors = ref<string[] | null>(null);
const checkError = ref('');
let checked = '';

watch([compose, env], () => {
  if (`${compose.value}\0${env.value}` !== checked) composeErrors.value = null;
});

async function check(): Promise<string[] | null> {
  checking.value = true;
  checkError.value = '';
  try {
    const text = `${compose.value}\0${env.value}`;
    const { errors } = await checkStack({ compose: compose.value, env: env.value });
    checked = text;
    composeErrors.value = errors;
    return errors;
  } catch (e) {
    checkError.value = e instanceof Error ? e.message : String(e);
    return null;
  } finally {
    checking.value = false;
  }
}

// validate answers whether the files may be saved: never with syntax errors; with
// compose errors only if the user confirms. A check that can't run doesn't block.
async function validate(): Promise<boolean> {
  if (syntax.value.errors.length) return false;
  const errors = await check();
  if (!errors?.length) return true;
  return window.confirm('Compose reports problems with this file. Save it anyway?');
}

defineExpose({ validate });
</script>

<template>
  <div class="editor">
    <label class="field">
      <span
        >compose.yaml <span v-if="composeFromGit" class="muted">(read-only: it comes from git)</span></span
      >
      <CodeEditor
        v-model="compose"
        language="yaml"
        size="tall"
        :disabled="disabled || composeFromGit"
        :aria-invalid="syntax.errors.length > 0"
      />
    </label>
    <ul v-if="syntax.errors.length" class="errors" role="alert" aria-label="YAML errors">
      <li v-for="e in syntax.errors" :key="e">{{ e }}</li>
    </ul>
    <div class="field">
      <div class="env-head">
        <label for="stack-env"
          >.env <span class="muted">(variables compose substitutes, one KEY=value per line)</span></label
        >
        <button type="button" :disabled="disabled" @click="envForm = true">Editor</button>
      </div>
      <CodeEditor id="stack-env" v-model="env" language="env" size="short" :disabled="disabled" />
    </div>
    <Teleport to="body">
      <SettingsDialog v-if="envForm" title=".env editor" wide @close="envForm = false">
        <EnvFormEditor
          :env="env"
          :load-example="loadEnvExample"
          @apply="applyEnvForm"
          @cancel="envForm = false"
        />
      </SettingsDialog>
    </Teleport>
    <div class="check">
      <button type="button" :disabled="disabled || checking || syntax.errors.length > 0" @click="check">
        {{ checking ? 'Checking…' : 'Check with compose' }}
      </button>
      <span v-if="composeErrors?.length === 0" class="ok" role="status">Compose accepts the file.</span>
    </div>
    <p v-if="checkError" class="error" role="alert">{{ checkError }}</p>
    <ul v-if="composeErrors?.length" class="errors" role="alert" aria-label="Compose errors">
      <li v-for="e in composeErrors" :key="e">{{ e }}</li>
    </ul>
  </div>
</template>

<style scoped>
.editor {
  display: grid;
  gap: 0.75rem;
}

.field {
  display: grid;
  gap: 0.3rem;
}

.env-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}

.errors {
  margin: 0;
  padding-left: 1.25rem;
  color: var(--danger);
  font-family: var(--mono);
  font-size: 0.85rem;
}

.check {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}

.ok {
  color: var(--ok);
}
</style>
