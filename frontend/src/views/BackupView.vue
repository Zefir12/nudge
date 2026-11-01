<script setup lang="ts">
import { computed, ref } from 'vue';

import { createBackup } from '../api/client';

// Matches the server's backup.MinPassphrase.
const minLength = 12;

const passphrase = ref('');
const confirm = ref('');
const busy = ref(false);
const error = ref('');
const saved = ref('');

const mismatch = computed(() => confirm.value !== '' && confirm.value !== passphrase.value);
const ready = computed(
  () => passphrase.value.length >= minLength && confirm.value === passphrase.value && !busy.value,
);

async function onBackup() {
  busy.value = true;
  error.value = '';
  saved.value = '';
  try {
    const { blob, name } = await createBackup(passphrase.value);
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
    saved.value = name;
    passphrase.value = '';
    confirm.value = '';
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <h1>Backup</h1>
  <p class="muted">
    A backup holds nudge's database (users, environments and agent credentials, deployments, webhooks, the
    audit log), the key to the stored registry passwords, and every stack's <code>compose.yaml</code> and
    <code>.env</code>. It doesn't hold your containers' volumes or data that stacks bind-mount.
  </p>

  <section class="card">
    <h2>Download a backup</h2>
    <p>
      The file is encrypted with this passphrase. Keep it somewhere safe: without it the backup can't be
      restored, and with it anyone can read the stored credentials.
    </p>
    <form class="backup" @submit.prevent="onBackup">
      <label>
        Passphrase
        <input
          v-model="passphrase"
          name="passphrase"
          type="password"
          autocomplete="new-password"
          :minlength="minLength"
          required
        />
      </label>
      <label>
        Repeat it
        <input v-model="confirm" name="confirm" type="password" autocomplete="new-password" required />
      </label>
      <button type="submit" class="primary" :disabled="!ready">
        {{ busy ? 'Creating…' : 'Download backup' }}
      </button>
    </form>
    <p class="muted">At least {{ minLength }} characters.</p>
    <p v-if="mismatch" class="error">The passphrases differ.</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="saved" aria-live="polite">Saved {{ saved }}.</p>
  </section>

  <section class="card">
    <h2>Restore</h2>
    <p>
      Restoring runs with the server stopped, on the data volume. Put the passphrase in a file and run the
      server image's <code>restore</code> command:
    </p>
    <pre><code>docker run --rm -v &lt;data volume&gt;:/data -v "$PWD:/backup" &lt;nudge image&gt; \
  restore -file /backup/nudge-&lt;time&gt;.nudgebak -passphrase-file /backup/passphrase</code></pre>
    <p class="muted">
      It refuses a data directory that already has data; <code>-force</code> moves that data to
      <code>/data/pre-restore-&lt;time&gt;</code> first. Agents keep working: their credentials are in the
      backup.
    </p>
  </section>
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

.backup {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.backup label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

pre {
  padding: 0.5rem 0.75rem;
  background: var(--bg);
  border-radius: var(--radius);
  overflow-x: auto;
}
</style>
