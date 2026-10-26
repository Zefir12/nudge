<script setup lang="ts">
import { computed, ref, watch } from 'vue';

import { checkStack } from '../api/client';
import { readCompose } from '../compose';

// StackEditor edits a stack's compose.yaml and .env. Syntax errors show while typing;
// compose's own check runs on demand and from validate(), which the views call before
// saving.
const compose = defineModel<string>('compose', { required: true });
const env = defineModel<string>('env', { required: true });
defineProps<{ disabled?: boolean }>();

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
      <span>compose.yaml</span>
      <textarea
        v-model="compose"
        class="code"
        rows="18"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        :disabled="disabled"
        :aria-invalid="syntax.errors.length > 0"
      ></textarea>
    </label>
    <ul v-if="syntax.errors.length" class="errors" role="alert" aria-label="YAML errors">
      <li v-for="e in syntax.errors" :key="e">{{ e }}</li>
    </ul>
    <label class="field">
      <span>.env <span class="muted">(variables compose substitutes, one KEY=value per line)</span></span>
      <textarea
        v-model="env"
        class="code"
        rows="4"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        :disabled="disabled"
      ></textarea>
    </label>
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

.code {
  width: 100%;
  font-family: var(--mono);
  font-size: 0.85rem;
  tab-size: 2;
  resize: vertical;
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
