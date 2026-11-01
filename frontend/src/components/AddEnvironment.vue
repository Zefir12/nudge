<script setup lang="ts">
import { ref } from 'vue';

import { createEnvironment, type JoinToken } from '../api/client';
import JoinTokenCard from './JoinTokenCard.vue';

// AddEnvironment is the add form in the dashboard's dialog: it creates an agent
// environment, then shows its join token once. created fires after a create so the
// dashboard can reload; done when the user dismisses the join token.
const emit = defineEmits<{ created: []; done: [] }>();

const name = ref('');
const busy = ref(false);
const error = ref('');
const shown = ref<{ name: string; joinToken: JoinToken } | null>(null);

async function onCreate() {
  busy.value = true;
  error.value = '';
  try {
    const env = await createEnvironment(name.value.trim());
    shown.value = { name: env.name, joinToken: env.join_token };
    name.value = '';
    emit('created');
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <JoinTokenCard v-if="shown" :name="shown.name" :join-token="shown.joinToken" @done="emit('done')" />
  <template v-else>
    <p class="muted">
      Each environment is a Docker host. Remote hosts run the agent (<code>nudge-agent</code>), which connects
      out to this server.
    </p>
    <form class="create" @submit.prevent="onCreate">
      <label>
        Name
        <input v-model="name" name="name" maxlength="64" placeholder="e.g. nas" required />
      </label>
      <button type="submit" class="primary" :disabled="busy">Add environment</button>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </template>
</template>

<style scoped>
.create {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.create label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}
</style>
