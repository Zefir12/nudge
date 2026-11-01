<script setup lang="ts">
import { onMounted, ref } from 'vue';

// SettingsDialog is a modal with a title and the settings in its slot; it closes on the
// close button, Escape or a click on the backdrop.
defineProps<{ title: string }>();
const emit = defineEmits<{ close: [] }>();

const dialog = ref<HTMLDialogElement | null>(null);

onMounted(() => dialog.value?.showModal?.());

function onClick(e: MouseEvent) {
  if (e.target === dialog.value) emit('close');
}
</script>

<template>
  <dialog ref="dialog" class="settings" :aria-label="title" @close="emit('close')" @click="onClick">
    <div class="body">
      <header>
        <h2>{{ title }}</h2>
        <button type="button" class="close" aria-label="Close" @click="emit('close')">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
        </button>
      </header>
      <slot />
    </div>
  </dialog>
</template>

<style scoped>
.settings {
  width: min(32rem, calc(100vw - 2rem));
  padding: 0;
  color: var(--text);
  background: var(--surface-solid);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-lg);
}

.settings::backdrop {
  background: rgb(0 0 0 / 55%);
  backdrop-filter: blur(2px);
}

.body {
  padding: 1rem 1.25rem 1.25rem;
}

header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.75rem;
}

h2 {
  margin: 0;
}

.close {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  padding: 0;
  color: var(--muted);
  background: transparent;
  border-color: transparent;
}

.close:hover:not(:disabled) {
  color: var(--text);
  border-color: transparent;
}

svg {
  width: 16px;
  height: 16px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.8;
  stroke-linecap: round;
}
</style>
