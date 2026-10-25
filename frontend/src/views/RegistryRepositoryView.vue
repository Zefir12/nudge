<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue';
import { RouterLink, useRoute } from 'vue-router';

import {
  deleteRegistryTag,
  getRegistry,
  getRegistryManifest,
  listRegistryTags,
  type RegistryManifest,
} from '../api/client';
import LoadState from '../components/LoadState.vue';
import RegistryTagDetail from '../components/RegistryTagDetail.vue';
import { useParams } from '../composables/params';
import { useLoad } from '../composables/useLoad';
import { currentUser } from '../session';

const { id } = useParams();
const route = useRoute();
const registryId = computed(() => Number(id.value));
const repository = computed(() => (typeof route.query.name === 'string' ? route.query.name : ''));

const { data: registry } = useLoad(id, () => getRegistry(registryId.value));
const {
  data: tags,
  error,
  reload,
} = useLoad(
  () => [id.value, repository.value] as const,
  ([, repo]) => listRegistryTags(registryId.value, repo),
);

// A tag's manifest is fetched only when its row is opened, so a long tag list costs one
// request on load, not one per tag.
interface Detail {
  manifest?: RegistryManifest;
  error?: string;
}
const open = ref(new Set<string>());
const details = reactive(new Map<string, Detail>());
const deleting = ref('');
const deleteError = ref('');

// Another repository (or registry) starts with every tag closed.
watch([id, repository], () => {
  details.clear();
  open.value = new Set();
  deleteError.value = '';
});

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
const isAdmin = computed(() => currentUser.value?.admin === true);

async function toggle(tag: string) {
  const set = new Set(open.value);
  if (set.has(tag)) {
    set.delete(tag);
    open.value = set;
    return;
  }
  set.add(tag);
  open.value = set;
  if (details.get(tag)?.manifest) return;
  details.set(tag, {});
  const repo = repository.value;
  let detail: Detail;
  try {
    detail = { manifest: await getRegistryManifest(registryId.value, repo, tag) };
  } catch (e) {
    detail = { error: message(e) };
  }
  // The answer for a repository left in the meantime is dropped.
  if (repo === repository.value) details.set(tag, detail);
}

// confirmText names the tag and, for a generic registry (which deletes the manifest),
// the other tags on its digest that go with it.
function confirmText(m: RegistryManifest): string {
  let text = `Delete the tag "${m.repository}:${m.tag}"? This cannot be undone.`;
  if (registry.value?.kind !== 'forgejo') {
    if (m.shared_tags.length)
      text += `\n\nThese tags share its digest and are deleted with it: ${m.shared_tags.join(', ')}.`;
    if (!m.shared_tags_complete)
      text += '\n\nNot every tag was checked: other tags on the same digest may go too.';
  }
  return text;
}

async function onDelete(m: RegistryManifest) {
  if (!window.confirm(confirmText(m))) return;
  deleting.value = m.tag;
  deleteError.value = '';
  try {
    await deleteRegistryTag(registryId.value, m.repository, m.tag, m.digest);
    // Shared tags may be gone too; drop every detail and reload the list.
    details.clear();
    open.value = new Set();
    await reload();
  } catch (e) {
    deleteError.value = message(e);
  } finally {
    deleting.value = '';
  }
}
</script>

<template>
  <p>
    <RouterLink :to="{ name: 'registry', params: { id } }">← {{ registry?.name ?? 'Registry' }}</RouterLink>
  </p>
  <h1 class="break">{{ repository }}</h1>
  <p v-if="registry" class="muted">
    <code>{{ registry.address }}/{{ repository }}</code>
  </p>
  <p v-if="deleteError" class="error" role="alert">{{ deleteError }}</p>

  <section class="card">
    <LoadState
      :loading="!tags"
      :error="error"
      :ready="!!tags"
      :empty="tags?.length === 0"
      empty-text="No tags."
      @retry="reload"
    >
      <ul class="tags">
        <li v-for="t in tags" :key="t.tag">
          <button type="button" class="tag" :aria-expanded="open.has(t.tag)" @click="toggle(t.tag)">
            {{ open.has(t.tag) ? '▾' : '▸' }} <code>{{ t.tag }}</code>
          </button>
          <div v-if="open.has(t.tag)" class="detail">
            <p v-if="details.get(t.tag)?.error" class="error">{{ details.get(t.tag)?.error }}</p>
            <p v-else-if="!details.get(t.tag)?.manifest" class="muted">Loading…</p>
            <RegistryTagDetail
              v-else
              :manifest="details.get(t.tag)!.manifest!"
              :can-delete="isAdmin"
              :busy="deleting === t.tag"
              @delete="onDelete(details.get(t.tag)!.manifest!)"
            />
          </div>
        </li>
      </ul>
    </LoadState>
  </section>
</template>

<style scoped>
.tags {
  list-style: none;
  margin: 0;
  padding: 0;
}

.tags li + li {
  border-top: 1px solid var(--border);
}

.tag {
  width: 100%;
  text-align: left;
  background: none;
  border: none;
  padding: 0.5rem 0;
}

.detail {
  padding: 0 0 0.75rem 1.25rem;
}
</style>
