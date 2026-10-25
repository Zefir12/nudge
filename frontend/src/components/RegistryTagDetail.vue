<script setup lang="ts">
import type { RegistryManifest } from '../api/client';
import { formatBytes, formatDate, shortId } from '../format';

// RegistryTagDetail shows a tag's manifest, with Delete for those allowed to.
defineProps<{ manifest: RegistryManifest; canDelete: boolean; busy: boolean }>();
defineEmits<{ delete: [] }>();

const platformName = (p: { os: string; architecture: string; variant?: string }) =>
  [p.os, p.architecture, p.variant].filter(Boolean).join('/');
</script>

<template>
  <dl>
    <dt>Digest</dt>
    <dd>
      <code :title="manifest.digest">{{ shortId(manifest.digest) }}</code>
    </dd>
    <template v-if="manifest.size !== undefined">
      <dt>Size</dt>
      <dd>{{ formatBytes(manifest.size) }}</dd>
    </template>
    <template v-if="manifest.created">
      <dt>Created</dt>
      <dd>{{ formatDate(manifest.created) }}</dd>
    </template>
    <template v-if="manifest.platforms.length">
      <dt>Platforms</dt>
      <dd>
        <span v-for="p in manifest.platforms" :key="p.digest" class="platform">
          {{ platformName(p) }} <span class="muted">({{ formatBytes(p.size) }})</span>
        </span>
      </dd>
    </template>
    <template v-if="manifest.shared_tags.length">
      <dt>Same digest</dt>
      <dd>{{ manifest.shared_tags.join(', ') }}</dd>
    </template>
  </dl>
  <button v-if="canDelete" type="button" class="danger" :disabled="busy" @click="$emit('delete')">
    Delete
  </button>
</template>

<style scoped>
dl {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 0.25rem 1rem;
  margin: 0 0 0.75rem;
}

dt {
  color: var(--muted);
}

dd {
  margin: 0;
}

.platform {
  display: inline-block;
  margin-right: 1rem;
}
</style>
