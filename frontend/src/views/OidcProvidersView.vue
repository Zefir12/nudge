<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';

import {
  createOidcProvider,
  deleteOidcProvider,
  listOidcProviders,
  updateOidcProvider,
  type OidcProvider,
  type OidcProviderInput,
} from '../api/client';
import CopyToken from '../components/CopyToken.vue';
import FoldCard from '../components/FoldCard.vue';

const providers = ref<OidcProvider[] | null>(null);
const callbackPrefix = ref('');
const error = ref('');
const busy = ref(false);
// editing is the provider the form changes, null when it adds one.
const editing = ref<OidcProvider | null>(null);
const emptyForm = () => ({
  id: '',
  name: '',
  issuer: '',
  clientId: '',
  clientSecret: '',
  adminRole: 'admin',
  userRole: 'user',
  requireMfa: false,
  ttlMinutes: 720,
});
const form = ref(emptyForm());
// idEdited stops the ID following the name once the admin typed their own.
const idEdited = ref(false);

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

// deriveId mirrors the server's default for an ID from a name.
const deriveId = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
    .replace(/-+$/, '');

const shownId = computed(() => (idEdited.value ? form.value.id : deriveId(form.value.name)));

// The redirect URI to register at the provider, which comes before its client ID and secret.
const callbackUrl = computed(() => {
  if (editing.value) return editing.value.callback_url;
  return callbackPrefix.value && shownId.value ? `${callbackPrefix.value}${shownId.value}/callback` : '';
});

// needsSecret: the stored secret only goes to the issuer and client it was saved for, so an
// edit that changes one must give it again (the API refuses otherwise).
const needsSecret = computed(() => {
  const old = editing.value;
  if (!old) return true;
  if (old.secret_unreadable) return true;
  const f = form.value;
  return f.issuer.trim() !== old.issuer || f.clientId.trim() !== old.client_id;
});

async function load() {
  try {
    const list = await listOidcProviders();
    providers.value = list.providers;
    callbackPrefix.value = list.callback_prefix;
  } catch (e) {
    error.value = message(e);
  }
}

function reset() {
  editing.value = null;
  idEdited.value = false;
  form.value = emptyForm();
}

function onEdit(p: OidcProvider) {
  editing.value = p;
  form.value = {
    id: p.id,
    name: p.name,
    issuer: p.issuer,
    clientId: p.client_id,
    clientSecret: '',
    adminRole: p.admin_role,
    userRole: p.user_role,
    requireMfa: p.require_mfa,
    ttlMinutes: p.session_ttl_minutes,
  };
  error.value = '';
}

function inputOf(p: OidcProvider): OidcProviderInput {
  return {
    name: p.name,
    issuer: p.issuer,
    client_id: p.client_id,
    admin_role: p.admin_role,
    user_role: p.user_role,
    require_mfa: p.require_mfa,
    session_ttl_minutes: p.session_ttl_minutes,
  };
}

async function onSubmit() {
  const f = form.value;
  if (editing.value && needsSecret.value && !f.clientSecret) {
    error.value = 'Enter the client secret again: the issuer or client ID changed.';
    return;
  }
  busy.value = true;
  error.value = '';
  const input: OidcProviderInput = {
    name: f.name.trim(),
    issuer: f.issuer.trim(),
    client_id: f.clientId.trim(),
    admin_role: f.adminRole.trim(),
    user_role: f.userRole.trim(),
    require_mfa: f.requireMfa,
    session_ttl_minutes: f.ttlMinutes,
  };
  // An empty secret on edit keeps the stored one.
  if (f.clientSecret) input.client_secret = f.clientSecret;
  try {
    if (editing.value) await updateOidcProvider(editing.value.id, input);
    else await createOidcProvider({ ...input, id: shownId.value });
    reset();
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function onToggle(p: OidcProvider) {
  if (
    p.enabled &&
    !window.confirm(
      `Disable ${p.name}? Its users are signed out and their API tokens are deleted; you can enable it again, but the tokens stay gone.`,
    )
  )
    return;
  error.value = '';
  try {
    await updateOidcProvider(p.id, { ...inputOf(p), enabled: !p.enabled });
    await load();
  } catch (e) {
    error.value = message(e);
  }
}

async function onDelete(p: OidcProvider) {
  if (
    !window.confirm(
      `Delete ${p.name}? Its users are signed out and their API tokens are deleted. Their accounts stay.`,
    )
  )
    return;
  error.value = '';
  try {
    await deleteOidcProvider(p.id);
    if (editing.value?.id === p.id) reset();
    await load();
  } catch (e) {
    error.value = message(e);
  }
}

onMounted(load);
</script>

<template>
  <h1>Single sign-on</h1>
  <p class="muted">
    Users sign in through these OpenID Connect providers, one button each on the login page, and are matched
    by issuer and subject. Client secrets are stored encrypted and never shown again.
  </p>

  <FoldCard fold-id="oidc-providers.form" :force-open="!!editing">
    <template #title>{{ editing ? `Edit ${editing.name}` : 'Add a provider' }}</template>
    <p v-if="providers && !callbackPrefix" class="warn">
      Single sign-on needs an https:// public URL (NUDGE_PUBLIC_URL) for its redirect URI.
    </p>
    <form class="provider" @submit.prevent="onSubmit">
      <label>
        Name (login button label)
        <input v-model="form.name" name="name" maxlength="64" placeholder="e.g. Authestra" required />
      </label>
      <label v-if="!editing">
        ID (in the callback URL; can't change later)
        <input
          :value="shownId"
          name="id"
          maxlength="40"
          pattern="[a-z0-9][a-z0-9\-]*"
          required
          @input="
            idEdited = true;
            form.id = ($event.target as HTMLInputElement).value;
          "
        />
      </label>
      <label>
        Issuer URL
        <input
          v-model="form.issuer"
          name="issuer"
          type="url"
          placeholder="https://auth.example.net"
          required
        />
      </label>
      <label>
        Client ID
        <input v-model="form.clientId" name="client_id" maxlength="255" autocomplete="off" required />
      </label>
      <label>
        Client secret
        <input
          v-model="form.clientSecret"
          name="client_secret"
          type="password"
          autocomplete="new-password"
          :placeholder="editing && !needsSecret ? 'unchanged' : ''"
          :required="needsSecret"
        />
      </label>
      <label>
        Admin role
        <input v-model="form.adminRole" name="admin_role" maxlength="255" required />
      </label>
      <label>
        User role (empty: admins only)
        <input v-model="form.userRole" name="user_role" maxlength="255" />
      </label>
      <label>
        Session length (minutes)
        <input
          v-model.number="form.ttlMinutes"
          name="session_ttl_minutes"
          type="number"
          min="5"
          max="43200"
          required
        />
      </label>
      <label class="check">
        <input v-model="form.requireMfa" name="require_mfa" type="checkbox" />
        Require a second factor
      </label>
      <div class="buttons">
        <button type="submit" class="primary" :disabled="busy">{{ editing ? 'Save' : 'Add' }}</button>
        <button v-if="editing" type="button" @click="reset">Cancel</button>
      </div>
    </form>
    <p v-if="callbackUrl" class="callback">
      Redirect URI to register at the provider: <code>{{ callbackUrl }}</code>
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </FoldCard>

  <section class="card">
    <p v-if="providers === null && !error" class="muted">Loading…</p>
    <p v-else-if="providers && providers.length === 0" class="muted">No providers yet.</p>
    <div v-else-if="providers" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Issuer</th>
            <th>Status</th>
            <th class="wide-only">Redirect URI</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in providers" :key="p.id">
            <td>{{ p.name }}</td>
            <td>
              <code>{{ p.issuer }}</code>
            </td>
            <td>
              <span v-if="p.secret_unreadable" class="warn">Enter the client secret again</span>
              <span v-else>{{ p.enabled ? 'Enabled' : 'Disabled' }}</span>
              <span v-if="p.read_only" class="muted"> (from NUDGE_OIDC_*)</span>
            </td>
            <td class="wide-only">
              <CopyToken v-if="p.callback_url" label="Copy" :values="[p.callback_url]" />
            </td>
            <td class="row-actions">
              <template v-if="!p.read_only">
                <button type="button" @click="onEdit(p)">Edit</button>
                <button type="button" @click="onToggle(p)">{{ p.enabled ? 'Disable' : 'Enable' }}</button>
                <button type="button" class="danger" @click="onDelete(p)">Delete</button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.card {
  margin-top: 1rem;
}

.provider {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  max-width: 32rem;
}

.provider label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.provider label.check {
  flex-direction: row;
  align-items: center;
  gap: 0.4rem;
  font-weight: normal;
}

.callback code {
  overflow-wrap: anywhere;
}

.warn {
  color: var(--warn);
}

.buttons,
.row-actions {
  display: flex;
  gap: 0.5rem;
}
</style>
