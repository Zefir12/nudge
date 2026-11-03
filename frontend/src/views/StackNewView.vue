<script setup lang="ts">
import { ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import { createStack, createStackFromGit, emptyStackGit } from '../api/client';
import CodeEditor from '../components/CodeEditor.vue';
import StackEditor from '../components/StackEditor.vue';
import StackGitForm from '../components/StackGitForm.vue';
import { useParams } from '../composables/params';

const { env } = useParams();
const router = useRouter();

const name = ref('');
const compose = ref('services:\n  app:\n    image: nginx:alpine\n    restart: unless-stopped\n');
const envFile = ref('');
const source = ref<'editor' | 'git'>('editor');
const git = ref(emptyStackGit());
const saving = ref(false);
const error = ref('');
const editor = ref<InstanceType<typeof StackEditor> | null>(null);

// Compose project names: lowercase letters, digits, - and _.
const namePattern = '[a-z0-9][a-z0-9_\\-]*';

async function onCreate() {
  error.value = '';
  saving.value = true;
  try {
    if (source.value === 'git') {
      await createStackFromGit(env.value, name.value.trim(), git.value, envFile.value);
    } else {
      if (!(await editor.value?.validate())) return;
      await createStack(env.value, name.value.trim(), { compose: compose.value, env: envFile.value });
    }
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
    <fieldset class="source">
      <legend>Compose file</legend>
      <label><input v-model="source" type="radio" value="editor" :disabled="saving" /> Write it here</label>
      <label
        ><input v-model="source" type="radio" value="git" :disabled="saving" /> From a git repository</label
      >
    </fieldset>
    <template v-if="source === 'git'">
      <StackGitForm v-model="git" :disabled="saving" />
      <label class="field">
        <span>.env <span class="muted">(variables that stay here, such as secrets; optional)</span></span>
        <CodeEditor v-model="envFile" language="env" size="short" :disabled="saving" />
      </label>
    </template>
    <StackEditor v-else ref="editor" v-model:compose="compose" v-model:env="envFile" :disabled="saving" />
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

.source {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
  margin: 0;
  padding: 0;
  border: 0;
}

.source legend {
  padding: 0 0 0.3rem;
}

.source label {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}
</style>
