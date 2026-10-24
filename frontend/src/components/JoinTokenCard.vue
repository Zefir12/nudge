<script setup lang="ts">
import { ref } from 'vue';

import type { JoinToken } from '../api/client';
import { formatDate } from '../format';

// JoinTokenCard shows the command that starts an agent with a fresh join token. The token
// is shown only once, so the card stays until the user dismisses it.
defineProps<{ name: string; joinToken: JoinToken }>();
const emit = defineEmits<{ done: [] }>();

const copyState = ref<'' | 'copied' | 'failed'>('');

async function onCopy(text: string) {
  copyState.value = '';
  try {
    await navigator.clipboard.writeText(text);
    copyState.value = 'copied';
  } catch {
    // No clipboard API (plain HTTP): the command stays selectable.
    copyState.value = 'failed';
  }
}
</script>

<template>
  <section class="card created" aria-live="polite">
    <h2>Start the agent for "{{ name }}"</h2>
    <p>
      Run this on the remote Docker host. It needs no inbound port: the agent connects to this server. The
      join token works once, until {{ formatDate(joinToken.expires_at) }}, and is shown only now.
    </p>
    <code class="command">{{ joinToken.docker_run }}</code>
    <div class="actions">
      <button type="button" @click="onCopy(joinToken.docker_run)">
        {{ copyState === 'copied' ? 'Copied' : 'Copy command' }}
      </button>
      <button type="button" @click="emit('done')">Done</button>
    </div>
    <p v-if="copyState === 'failed'" class="muted">
      Copying is not available here: select the command and copy it.
    </p>
  </section>
</template>

<style scoped>
.created {
  border-color: var(--accent);
}

.command {
  display: block;
  padding: 0.5rem 0.75rem;
  background: var(--bg);
  border-radius: var(--radius);
  word-break: break-all;
  user-select: all;
}

.actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.75rem;
}
</style>
