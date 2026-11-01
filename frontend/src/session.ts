// The logged-in user, shared by the router guard and the layout.
import { ref } from 'vue';

import { ApiError, getMe, type User } from './api/client';

export const currentUser = ref<User | null>(null);

// apiError is set when the router guard couldn't reach the API; App shows it, with
// apiErrorPath, the page it was going to, to load once the API answers again.
export const apiError = ref('');
export const apiErrorPath = ref('');

// loadUser asks the server who is logged in; null when nobody is.
export async function loadUser(): Promise<User | null> {
  try {
    currentUser.value = await getMe();
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) throw e;
    currentUser.value = null;
  }
  return currentUser.value;
}

// safeRedirect keeps a ?redirect= target only when it is a path on this site, so a
// crafted login link can't send the user elsewhere after login.
export function safeRedirect(target: unknown): string {
  if (
    typeof target !== 'string' ||
    !target.startsWith('/') ||
    target.startsWith('//') ||
    target.includes('\\')
  ) {
    return '/';
  }
  return target;
}
