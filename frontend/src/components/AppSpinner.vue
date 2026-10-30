<script setup lang="ts">
// AppSpinner is a small turning ring for work without a known end; the text next to it
// says what runs.
</script>

<template>
  <span class="spinner" aria-hidden="true" />
</template>

<style scoped>
.spinner {
  display: inline-block;
  flex: none;
  width: var(--spinner-size, 1rem);
  height: var(--spinner-size, 1rem);
  vertical-align: -0.15em;
  border: 2px solid var(--border-strong);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@media (prefers-reduced-motion: reduce) {
  .spinner {
    animation-duration: 2.4s;
  }
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
