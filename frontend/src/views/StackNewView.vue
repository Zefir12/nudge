<script setup lang="ts">
import { ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import { createStack } from '../api/client';
import StackEditor from '../components/StackEditor.vue';
import { useParams } from '../composables/params';

const { env } = useParams();
const router = useRouter();

const name = ref('');
const compose = ref('services:\n  app:\n    image: nginx:alpine\n    restart: unless-stopped\n');
const envFile = ref('');
const saving = ref(false);
const error = ref('');
const editor = ref<InstanceType<typeof StackEditor> | null>(null);

// Compose project names: lowercase letters, digits, - and _.
const namePattern = '[a-z0-9][a-z0-9_\\-]*';

async function onCreate() {
  error.value = '';
  saving.value = true;
  try {
    if (!(await editor.value?.validate())) return;
    await createStack(env.value, name.value.trim(), { compose: compose.value, env: envFile.value });
    await router.push({ name: 'stack', params: { env: env.value, id: name.value.trim() } });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <p><RouterLink :to="{ name: 'stacks', params: { env } }">← Stacks</RouterLink></p>
  <h1>New stack</h1>
  <form class="card" @submit.prevent="onCreate">
    <label class="field">
      <span>Name</span>
      <input
        v-model="name"
        required
        maxlength="63"
        :pattern="namePattern"
        autocapitalize="off"
        spellcheck="false"
        placeholder="web"
        :disabled="saving"
      />
      <span class="muted">Lowercase letters, digits, - and _; it names the compose project.</span>
    </label>
    <StackEditor ref="editor" v-model:compose="compose" v-model:env="envFile" :disabled="saving" />
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="actions">
      <button type="submit" class="primary" :disabled="saving">{{ saving ? 'Creating…' : 'Create' }}</button>
      <span class="muted">The stack is deployed from its page.</span>
    </div>
  </form>
</template>

<style scoped>
form {
  display: grid;
  gap: 0.75rem;
}

.field {
  display: grid;
  gap: 0.3rem;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}
</style>
