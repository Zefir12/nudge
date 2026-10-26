<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import { ApiError, getRegistry, listRegistryRepositories } from '../api/client';
import LoadState from '../components/LoadState.vue';
import { useParams } from '../composables/params';
import { useLoad } from '../composables/useLoad';

const { id } = useParams();
const registryId = computed(() => Number(id.value));
const { data: registry, error, reload } = useLoad(id, () => getRegistry(registryId.value));

const router = useRouter();
const repos = ref<string[]>([]);
const next = ref('');
const query = ref('');
const loading = ref(false);
const listError = ref('');
// unlisted: the registry has no catalog (Docker Hub, GHCR); repositories open by name.
const unlisted = ref(false);
const loaded = ref(false);
const openName = ref('');
let seq = 0;

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

// load fetches the first page for the current search, or with more the next page.
async function load(more = false) {
  const mine = ++seq;
  loading.value = true;
  listError.value = '';
  try {
    const page = await listRegistryRepositories(registryId.value, query.value.trim(), more ? next.value : '');
    if (mine !== seq) return;
    const names = page.repositories.map((r) => r.name);
    repos.value = more ? [...repos.value, ...names] : names;
    next.value = page.next ?? '';
    loaded.value = true;
  } catch (e) {
    if (mine !== seq) return;
    if (e instanceof ApiError && e.status === 409 && e.message.includes('open one by name'))
      unlisted.value = true;
    else listError.value = message(e);
  } finally {
    if (mine === seq) loading.value = false;
  }
}

watch(
  id,
  (v) => {
    if (!v) return;
    repos.value = [];
    next.value = '';
    query.value = '';
    unlisted.value = false;
    loaded.value = false;
    void load();
  },
  { immediate: true },
);

function onOpen() {
  const name = openName.value.trim();
  if (!name) return;
  void router.push({ name: 'registry-repository', params: { id: id.value }, query: { name } });
}
</script>

<template>
  <p><RouterLink :to="{ name: 'registries' }">← Registries</RouterLink></p>
  <LoadState :loading="!registry" :error="error" :ready="!!registry" @retry="reload">
    <template v-if="registry">
      <h1>{{ registry.name }}</h1>
      <p class="muted">
        <code>{{ registry.address }}</code>
        · {{ registry.kind === 'forgejo' ? 'Forgejo' : 'Registry v2' }}
      </p>

      <section class="card">
        <form class="bar" @submit.prevent="onOpen">
          <label>
            Open a repository by name
            <input v-model="openName" name="open" placeholder="team/app" required />
          </label>
          <button type="submit">Open</button>
        </form>
      </section>

      <section class="card">
        <p v-if="unlisted" class="muted">
          This registry doesn't list its repositories. Open one by name above (on Docker Hub,
          <code>nginx</code> is <code>library/nginx</code>).
        </p>
        <template v-else>
          <form class="bar" @submit.prevent="load()">
            <input
              v-model="query"
              name="q"
              type="search"
              placeholder="Search repositories"
              aria-label="Search"
            />
            <button type="submit" :disabled="loading">Search</button>
          </form>
          <p v-if="listError" class="error" role="alert">{{ listError }}</p>
          <p v-else-if="!loaded" class="muted">Loading…</p>
          <p v-else-if="repos.length === 0" class="muted">No repositories.</p>
          <ul v-if="repos.length" class="repos">
            <li v-for="name in repos" :key="name">
              <RouterLink :to="{ name: 'registry-repository', params: { id }, query: { name } }">{{
                name
              }}</RouterLink>
            </li>
          </ul>
          <button v-if="next && !listError" type="button" :disabled="loading" @click="load(true)">
            Load more
          </button>
        </template>
      </section>
    </template>
  </LoadState>
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

.bar {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.bar label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.repos {
  padding-left: 1.25rem;
  font-family: var(--mono);
}

.repos li + li {
  margin-top: 0.25rem;
}
</style>
