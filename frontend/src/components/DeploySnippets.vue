<!-- CI steps that deploy a deployment and fail when the run does: curl on the progress
     stream, or `nudge deploy`. -->
<script setup lang="ts">
import { computed, ref } from 'vue';

const props = defineProps<{ name: string; variables: string[] }>();

const kind = ref<'curl' | 'cli'>('curl');
const server = window.location.origin;
const placeholder = (v: string) => `<${v.toLowerCase()}>`;

// The curl step prints the progress and passes only if the last line says succeeded.
const curl = computed(() => {
  const body = JSON.stringify({
    variables: Object.fromEntries(props.variables.map((v) => [v, placeholder(v)])),
  });
  return `- name: Deploy
  env:
    NUDGE_DEPLOY_TOKEN: \${{ secrets.NUDGE_DEPLOY_TOKEN }}
  run: |
    curl -fsSN -X POST \\
      -H "Authorization: Bearer $NUDGE_DEPLOY_TOKEN" \\
      -H "Content-Type: application/json" \\
      -d '${body}' \\
      "${server}/api/v1/deployments/${props.name}/deploys?stream=1" | tee progress.jsonl
    tail -n 1 progress.jsonl | grep -q '"status":"succeeded"'`;
});

// The same step with the nudge image's deploy command, which exits 0 only on success.
const cli = computed(() => {
  const vars = props.variables.map((v) => ` \\\n      --var ${v}=${placeholder(v)}`).join('');
  return `- name: Deploy
  env:
    NUDGE_DEPLOY_TOKEN: \${{ secrets.NUDGE_DEPLOY_TOKEN }}
  run: |
    docker run --rm -e NUDGE_DEPLOY_TOKEN <nudge image> deploy \\
      --server ${server} --deployment ${props.name}${vars}`;
});
</script>

<template>
  <div class="snippets">
    <h3>CI step</h3>
    <p class="muted">
      For Forgejo or GitHub Actions; store a deploy token as the secret <code>NUDGE_DEPLOY_TOKEN</code>.
    </p>
    <div class="tabs" role="tablist">
      <button
        type="button"
        role="tab"
        :aria-selected="kind === 'curl'"
        :class="{ on: kind === 'curl' }"
        @click="kind = 'curl'"
      >
        curl
      </button>
      <button
        type="button"
        role="tab"
        :aria-selected="kind === 'cli'"
        :class="{ on: kind === 'cli' }"
        @click="kind = 'cli'"
      >
        nudge deploy
      </button>
    </div>
    <pre class="snippet">{{ kind === 'curl' ? curl : cli }}</pre>
  </div>
</template>

<style scoped>
.snippets {
  margin: 1rem 0;
}

h3,
p {
  margin: 0 0 0.5rem;
}

.tabs {
  display: flex;
  gap: 0.25rem;
}

.tabs .on {
  border-color: var(--accent-border);
  color: var(--accent-strong);
}

.snippet {
  display: block;
  overflow-x: auto;
  margin: 0.5rem 0;
  white-space: pre;
}
</style>
