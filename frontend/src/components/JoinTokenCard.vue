<script setup lang="ts">
import { ref } from 'vue';

import type { JoinToken } from '../api/client';
import { formatDate } from '../format';

// JoinTokenCard shows the command, and the same agent as a compose file, that start an
// agent with a fresh join token. The token is shown only once, so the card stays until
// the user dismisses it.
defineProps<{ name: string; joinToken: JoinToken }>();
const emit = defineEmits<{ done: [] }>();

type Snippet = 'command' | 'compose';

const copied = ref<Snippet | ''>('');
const copyFailed = ref(false);

async function onCopy(which: Snippet, text: string) {
  copied.value = '';
  copyFailed.value = false;
  try {
    await navigator.clipboard.writeText(text);
    copied.value = which;
  } catch {
    // No clipboard API (plain HTTP): the snippets stay selectable.
    copyFailed.value = true;
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
      <button type="button" @click="onCopy('command', joinToken.docker_run)">
        {{ copied === 'command' ? 'Copied' : 'Copy command' }}
      </button>
    </div>

    <h3>Or with Docker Compose</h3>
    <p>Save this as <code>compose.yaml</code> and run <code>docker compose up -d</code> next to it.</p>
    <pre class="compose">{{ joinToken.compose }}</pre>
    <div class="actions">
      <button type="button" @click="onCopy('compose', joinToken.compose)">
        {{ copied === 'compose' ? 'Copied' : 'Copy compose file' }}
      </button>
    </div>

    <p v-if="copyFailed" class="muted">Copying is not available here: select the text and copy it.</p>
    <div class="actions">
      <button type="button" @click="emit('done')">Done</button>
    </div>
  </section>
</template>

<style scoped>
.created {
  border-color: var(--accent);
}

.command,
.compose {
  display: block;
  margin: 0;
  padding: 0.5rem 0.75rem;
  background: var(--bg);
  border-radius: var(--radius);
  user-select: all;
}

.command {
  word-break: break-all;
}

.compose {
  overflow-x: auto;
}

.actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.75rem;
}
</style>
