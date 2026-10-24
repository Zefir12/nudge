import { computed, ref, type Ref } from 'vue';

import { checkUpdates, listUpdates, type ImageUpdate } from '../api/client';
import { useLoad } from './useLoad';

const key = (image: string, imageId: string) => `${image}\n${imageId}`;

// useUpdates loads the environment's last update check. A failed load only hides the
// badges, so lists still work when checks aren't available.
export function useUpdates(env: Readonly<Ref<string>>) {
  const { data, reload } = useLoad(env, listUpdates);
  const byImage = computed(() => new Map((data.value ?? []).map((u) => [key(u.image, u.image_id), u])));
  // updateFor is the check of what a container runs, if an update is available.
  const updateFor = (c: { image: string; image_id: string }): ImageUpdate | undefined => {
    const u = byImage.value.get(key(c.image, c.image_id));
    return u?.status === 'available' ? u : undefined;
  };

  const checking = ref(false);
  const checkError = ref('');
  async function check() {
    checking.value = true;
    checkError.value = '';
    try {
      data.value = await checkUpdates(env.value);
    } catch (e) {
      checkError.value = e instanceof Error ? e.message : String(e);
    } finally {
      checking.value = false;
    }
  }
  return { updates: data, updateFor, check, checking, checkError, reload };
}
