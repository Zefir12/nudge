<script setup lang="ts">
import { computed } from 'vue';

import { isFolded, setFolded } from '../folds';

// FoldCard is a card with a title that folds away to just that title. foldId names the kind
// of card, and the choice is remembered in this browser for every card with that ID.
// forceOpen shows the body regardless and locks the toggle, for when an action fills it in
// or it holds something the user can't get back.
const props = defineProps<{ foldId: string; forceOpen?: boolean }>();

const folded = computed(() => isFolded(props.foldId) && !props.forceOpen);
</script>

<template>
  <section class="card" :class="{ folded }">
    <div class="fold-head">
      <button
        type="button"
        class="fold-toggle"
        :aria-expanded="!folded"
        :aria-label="folded ? 'Unfold' : 'Fold'"
        :title="folded ? 'Unfold' : 'Fold'"
        :disabled="forceOpen"
        @click="setFolded(foldId, !folded)"
      >
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 9l6 6l6-6" /></svg>
      </button>
      <h2><slot name="title" /></h2>
      <div v-if="$slots.actions" class="fold-actions"><slot name="actions" /></div>
    </div>
    <div v-show="!folded"><slot /></div>
  </section>
</template>

<style scoped>
.fold-head {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  margin-bottom: 0.75rem;
}

.folded .fold-head {
  margin-bottom: 0;
}

.fold-head h2 {
  margin: 0;
}

.fold-actions {
  margin-left: auto;
}

.fold-toggle {
  display: grid;
  flex: none;
  place-items: center;
  width: 22px;
  height: 22px;
  margin-left: -0.3rem;
  padding: 0;
  color: var(--muted);
  background: transparent;
  border-color: transparent;
}

.fold-toggle:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--border-strong);
}

.fold-toggle svg {
  width: 16px;
  height: 16px;
  fill: none;
  stroke: currentcolor;
  stroke-width: 2;
  stroke-linecap: round;
  stroke-linejoin: round;
  transition: transform 0.15s ease;
}

.folded .fold-toggle svg {
  transform: rotate(-90deg);
}
</style>
