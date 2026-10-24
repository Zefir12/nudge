// The environments the user can switch between, and the one used last.
import { ref } from 'vue';

import { listEnvironments, type Environment } from './api/client';

export const environments = ref<Environment[] | null>(null);

const lastKey = 'nudge.lastEnvironment';

export async function loadEnvironments(): Promise<Environment[]> {
  environments.value = await listEnvironments();
  return environments.value;
}

// lastEnvironment is the ID remembered in this browser; storage may be unavailable.
export function lastEnvironment(): string | null {
  try {
    return localStorage.getItem(lastKey);
  } catch {
    return null;
  }
}

export function rememberEnvironment(id: string) {
  try {
    localStorage.setItem(lastKey, id);
  } catch {
    // Private mode or blocked storage: the switcher still works, it just isn't remembered.
  }
}

// defaultEnvironment picks the remembered environment if it still exists, else the first.
export function defaultEnvironment(envs: Environment[]): Environment | undefined {
  const last = lastEnvironment();
  return envs.find((e) => String(e.id) === last) ?? envs[0];
}
