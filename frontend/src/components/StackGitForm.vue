<script setup lang="ts">
import { computed } from 'vue';

import type { StackGitInput } from '../api/client';

// StackGitForm edits where a stack's compose file comes from. A secret left empty keeps the
// stored one, which the server only allows while it would go to the same place.
const input = defineModel<StackGitInput>({ required: true });
defineProps<{ disabled?: boolean; hasSecret?: boolean }>();

const pollMinutes = computed({
  get: () => (input.value.poll_seconds ? String(input.value.poll_seconds / 60) : ''),
  set: (v: string) => (input.value = { ...input.value, poll_seconds: Math.round(Number(v) * 60) || 0 }),
});
</script>

<template>
  <div class="git-form">
    <label class="field">
      <span>Repository URL</span>
      <input
        v-model.trim="input.repo_url"
        required
        maxlength="2048"
        autocapitalize="off"
        spellcheck="false"
        placeholder="https://git.example.com/me/app.git"
        :disabled="disabled"
      />
    </label>
    <div class="row">
      <label class="field">
        <span>Branch or tag <span class="muted">(empty: the default branch)</span></span>
        <input
          v-model.trim="input.ref"
          maxlength="255"
          spellcheck="false"
          placeholder="main"
          :disabled="disabled"
        />
      </label>
      <label class="field">
        <span>Compose file</span>
        <input
          v-model.trim="input.compose_path"
          required
          maxlength="1024"
          spellcheck="false"
          placeholder="deploy/compose.yaml"
          :disabled="disabled"
        />
      </label>
      <label class="field">
        <span>Env file <span class="muted">(optional)</span></span>
        <input
          v-model.trim="input.env_path"
          maxlength="1024"
          spellcheck="false"
          placeholder="deploy/app.env"
          :disabled="disabled"
        />
      </label>
    </div>
    <p v-if="input.env_path" class="muted">
      Its variables are set in the stack's .env at every pull. Other variables there, such as secrets, stay.
    </p>
    <div class="row">
      <label class="field">
        <span>Log in with</span>
        <select v-model="input.auth" :disabled="disabled">
          <option value="none">Nothing (public repository)</option>
          <option value="token">Token over HTTPS</option>
          <option value="ssh">SSH key</option>
        </select>
      </label>
      <label v-if="input.auth !== 'none'" class="field">
        <span>Username <span class="muted">(default git)</span></span>
        <input v-model.trim="input.username" maxlength="255" spellcheck="false" :disabled="disabled" />
      </label>
      <label class="field">
        <span>Check every <span class="muted">(minutes; empty: never)</span></span>
        <input v-model="pollMinutes" type="number" min="1" max="1440" step="1" :disabled="disabled" />
      </label>
    </div>
    <label v-if="input.auth === 'token'" class="field">
      <span>Token</span>
      <input
        v-model="input.secret"
        type="password"
        autocomplete="off"
        :required="!hasSecret"
        :placeholder="hasSecret ? 'Leave empty to keep the stored one' : ''"
        :disabled="disabled"
      />
    </label>
    <template v-if="input.auth === 'ssh'">
      <label class="field">
        <span>Private key <span class="muted">(PEM, without a passphrase)</span></span>
        <textarea
          v-model="input.secret"
          rows="4"
          class="mono"
          autocomplete="off"
          spellcheck="false"
          :required="!hasSecret"
          :placeholder="hasSecret ? 'Leave empty to keep the stored one' : ''"
          :disabled="disabled"
        />
      </label>
      <label class="field">
        <span>known_hosts <span class="muted">(the output of ssh-keyscan for the git server)</span></span>
        <textarea
          v-model="input.known_hosts"
          rows="2"
          class="mono"
          required
          spellcheck="false"
          :disabled="disabled"
        />
      </label>
    </template>
    <p class="muted">The token or key is stored encrypted and never shown again.</p>
  </div>
</template>

<style scoped>
.git-form {
  display: grid;
  gap: 0.75rem;
}

.field {
  display: grid;
  gap: 0.3rem;
  align-content: start;
}

.row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
  gap: 0.75rem;
}

.mono {
  font-family: var(--mono);
  font-size: 0.85rem;
}

p {
  margin: 0;
}
</style>
