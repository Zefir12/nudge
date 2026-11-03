<script setup lang="ts">
import { onUnmounted, ref } from 'vue';

// CopyToken stands in for long values: a short label that names them all when hovered and
// copies them, one per line, when clicked.
const props = defineProps<{ label: string; values: string[] }>();

const state = ref<'idle' | 'copied' | 'failed'>('idle');
let timer: ReturnType<typeof setTimeout> | undefined;
onUnmounted(() => clearTimeout(timer));

// legacyCopy is for plain HTTP, where the browser has no clipboard API.
function legacyCopy(text: string): boolean {
  const area = document.createElement('textarea');
  area.value = text;
  area.style.position = 'fixed';
  area.style.opacity = '0';
  document.body.append(area);
  area.focus();
  area.select();
  try {
    return document.execCommand('copy');
  } catch {
    return false;
  } finally {
    area.remove();
  }
}

async function copy(event: MouseEvent) {
  const text = props.values.join('\n');
  let ok = true;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    ok = legacyCopy(text);
    (event.currentTarget as HTMLElement | null)?.focus();
  }
  state.value = ok ? 'copied' : 'failed';
  clearTimeout(timer);
  timer = setTimeout(() => (state.value = 'idle'), 1500);
}
</script>

<template>
  <button type="button" class="token" :class="state" :title="values.join('\n')" @click="copy">
    {{ state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : label }}
  </button>
</template>

<style scoped>
.token {
  max-width: 14rem;
  overflow: hidden;
  padding: 0.1rem 0.45rem;
  font-family: var(--mono);
  font-size: 11.5px;
  font-weight: 400;
  color: var(--text-soft);
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: copy;
}

.token.copied {
  color: var(--ok-text);
}

.token.failed {
  color: var(--danger-text);
}
</style>
