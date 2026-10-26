<script setup lang="ts">
import { computed, ref } from 'vue';

import type { Credentials } from '../api/client';

const props = defineProps<{
  submitLabel: string;
  // confirm asks for the password twice and enforces the server's minimum length.
  confirm?: boolean;
  busy?: boolean;
  error?: string;
}>();

const emit = defineEmits<{ submit: [Credentials] }>();

const minPasswordLength = 8;

const username = ref('');
const password = ref('');
const repeat = ref('');
const localError = ref('');

const shownError = computed(() => localError.value || props.error || '');

function onSubmit() {
  localError.value = '';
  if (props.confirm) {
    if ([...password.value].length < minPasswordLength) {
      localError.value = `The password needs at least ${minPasswordLength} characters.`;
      return;
    }
    if (password.value !== repeat.value) {
      localError.value = 'The passwords do not match.';
      return;
    }
  }
  emit('submit', { username: username.value.trim(), password: password.value });
}
</script>

<template>
  <form class="credentials" @submit.prevent="onSubmit">
    <label>
      Username
      <input v-model="username" name="username" autocomplete="username" required />
    </label>
    <label>
      Password
      <input
        v-model="password"
        name="password"
        type="password"
        :autocomplete="confirm ? 'new-password' : 'current-password'"
        required
      />
    </label>
    <label v-if="confirm">
      Repeat password
      <input v-model="repeat" name="repeat" type="password" autocomplete="new-password" required />
    </label>
    <p v-if="shownError" class="error" role="alert">{{ shownError }}</p>
    <button type="submit" class="primary" :disabled="busy">{{ submitLabel }}</button>
  </form>
</template>

<style scoped>
.credentials {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.error {
  margin: 0;
}
</style>
