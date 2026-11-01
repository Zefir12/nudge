import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue';

import { streamLiveUsage, type LiveUsage } from '../api/client';
import { useStream } from './useStream';

// useLiveUsage follows env's live usage stream for as long as the calling component
// lives (the server samples env faster meanwhile), reconnecting with backoff, and closes
// it while the tab is hidden so a background tab doesn't keep the host busy. usage is the
// latest sample, null until one arrives and after env changes; error is why the last
// stream ended while a reconnect is pending. An empty env opens nothing.
export function useLiveUsage(env: Readonly<Ref<string>>) {
  const usage = shallowRef<LiveUsage | null>(null);
  const hidden = ref(document.visibilityState === 'hidden');
  const onVisibility = () => (hidden.value = document.visibilityState === 'hidden');
  document.addEventListener('visibilitychange', onVisibility);
  onScopeDispose(() => document.removeEventListener('visibilitychange', onVisibility));
  watch(env, () => (usage.value = null));
  const { error } = useStream<LiveUsage>(
    computed(() => (hidden.value ? '' : env.value)),
    (e, onItem, onEnd) => streamLiveUsage(e, { onUsage: onItem, onEnd }),
    (u) => (usage.value = u),
    'The usage stream ended.',
  );
  return { usage, error };
}
