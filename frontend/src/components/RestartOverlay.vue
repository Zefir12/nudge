<script setup lang="ts">
import { nextTick, ref, watch } from 'vue';

import { page, restarting } from '../reconnect';
import AppSpinner from './AppSpinner.vue';

// RestartOverlay covers the page while the server restarts, so nothing is clicked against
// a server that isn't there. A modal dialog, so it lies above the Settings dialog too.
const dialog = ref<HTMLDialogElement | null>(null);

watch(
  () => restarting.value !== null,
  async (open) => {
    if (!open) return;
    await nextTick();
    if (!dialog.value?.open) dialog.value?.showModal?.();
  },
  { immediate: true },
);
</script>

<template>
  <dialog v-if="restarting" ref="dialog" class="overlay" @cancel.prevent>
    <section class="restart" role="status" aria-live="polite">
      <AppSpinner class="big" />
      <div>
        <h2>{{ restarting.title }}</h2>
        <p class="muted">{{ restarting.detail }}</p>
        <button v-if="restarting.slow" type="button" class="reload" @click="page.reload()">
          Reload page
        </button>
      </div>
    </section>
  </dialog>
</template>

<style scoped>
.overlay {
  width: min(420px, calc(100vw - 2rem));
  padding: 1.25rem;
  color: var(--text);
  background: var(--surface-solid);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-lg);
}

.overlay::backdrop {
  background: rgb(0 0 0 / 60%);
  backdrop-filter: blur(2px);
}

.restart {
  display: flex;
  gap: 1rem;
  align-items: center;
}

.big {
  --spinner-size: 1.75rem;
}

h2 {
  margin: 0 0 0.25rem;
  font-size: 15px;
}

p {
  margin: 0;
}

.reload {
  margin-top: 0.75rem;
}
</style>
