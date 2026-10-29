<script setup lang="ts">
import { ref } from 'vue';

import {
  createJoinToken,
  deleteEnvironment,
  revokeAgent,
  type Environment,
  type JoinToken,
} from '../api/client';
import { formatDate } from '../format';
import JoinTokenCard from './JoinTokenCard.vue';

// EnvironmentSettings manages one environment in its tile's dialog: a new join token,
// revoking the agent and removing the environment, each after a confirm. changed fires
// after every action, failed or not, so the dashboard reloads the list.
const props = defineProps<{ env: Environment }>();
const emit = defineEmits<{ changed: [] }>();

const busy = ref(false);
const error = ref('');
// joinToken is shown until the user dismisses it; it is never shown again.
const joinToken = ref<JoinToken | null>(null);

// run does one action; the buttons stay disabled until it ends.
async function run(action: () => Promise<void>) {
  busy.value = true;
  error.value = '';
  try {
    await action();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
    emit('changed');
  }
}

function onNewToken() {
  const env = props.env;
  const note = env.agent?.enrolled
    ? 'The connected agent keeps working until an agent enrolls with the new token.'
    : 'Any earlier join token stops working.';
  if (!window.confirm(`Create a new join token for "${env.name}"? ${note}`)) return;
  void run(async () => {
    joinToken.value = await createJoinToken(env.id);
  });
}

function onRevoke() {
  const env = props.env;
  const msg = `Revoke the agent of "${env.name}"? It is disconnected and needs a new join token to connect again.`;
  if (!window.confirm(msg)) return;
  void run(async () => {
    await revokeAgent(env.id);
    joinToken.value = null;
  });
}

// onRemove asks for the name, so a slip of the mouse can't remove the wrong environment.
function onRemove() {
  const env = props.env;
  const typed = window.prompt(
    `Remove "${env.name}" from nudge? Its agent is disconnected and its stacks, webhooks and history ` +
      `here are deleted. Containers on the host keep running. Type the name to confirm:`,
  );
  if (typed === null) return;
  if (typed.trim() !== env.name) {
    error.value = `The name didn't match; "${env.name}" was not removed.`;
    return;
  }
  void run(async () => {
    await deleteEnvironment(env.id);
    joinToken.value = null;
  });
}
</script>

<template>
  <p v-if="env.kind === 'local'" class="muted">
    The local environment is this server's Docker socket: it has no agent to manage.
  </p>
  <template v-else>
    <JoinTokenCard v-if="joinToken" :name="env.name" :join-token="joinToken" @done="joinToken = null" />
    <dl class="facts">
      <dt>Agent</dt>
      <dd>{{ env.agent?.enrolled ? env.agent.version || 'enrolled' : 'not enrolled' }}</dd>
      <dt>Created</dt>
      <dd>{{ formatDate(env.created_at) }}</dd>
    </dl>
    <div class="actions">
      <button type="button" :disabled="busy" @click="onNewToken">New join token</button>
      <button v-if="env.agent?.enrolled" type="button" class="danger" :disabled="busy" @click="onRevoke">
        Revoke agent
      </button>
      <button type="button" class="danger" :disabled="busy" @click="onRemove">Remove</button>
    </div>
  </template>
  <p v-if="error" class="error" role="alert">{{ error }}</p>
</template>

<style scoped>
.created {
  margin-bottom: 1rem;
}

.facts {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.25rem 1rem;
  margin: 0 0 1rem;
}

.facts dt {
  color: var(--muted);
}

.facts dd {
  margin: 0;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
</style>
