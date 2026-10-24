<script setup lang="ts">
// KeyValues lists label/value pairs; empty values show a dash.
export type Row = [string, string | number | boolean | null | undefined];

defineProps<{ rows: Row[]; mono?: boolean }>();

const show = (v: Row[1]) => {
  if (v === null || v === undefined || v === '') return '—';
  if (typeof v === 'boolean') return v ? 'yes' : 'no';
  return String(v);
};
</script>

<template>
  <dl :class="{ mono }">
    <template v-for="[k, v] in rows" :key="k">
      <dt>{{ k }}</dt>
      <dd>{{ show(v) }}</dd>
    </template>
  </dl>
</template>

<style scoped>
dl {
  display: grid;
  grid-template-columns: minmax(8rem, max-content) 1fr;
  gap: 0.35rem 1rem;
  margin: 0;
}

dt {
  color: var(--muted);
}

dd {
  margin: 0;
  word-break: break-all;
}

.mono dt,
.mono dd {
  font-family: var(--mono);
  font-size: 0.9em;
}

@media (max-width: 720px) {
  dl {
    grid-template-columns: 1fr;
    gap: 0 0;
  }

  dd {
    margin-bottom: 0.5rem;
  }
}
</style>
