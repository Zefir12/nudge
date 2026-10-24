import { ref, watch, type Ref, type WatchSource } from 'vue';

export interface Load<T> {
  data: Ref<T | null>;
  error: Ref<string>;
  loading: Ref<boolean>;
  reload: () => Promise<void>;
}

// useLoad runs fetch now and whenever source changes (the route's environment or ID), and
// drops answers that arrive after a newer request started. Empty sources are skipped.
export function useLoad<T, S>(source: WatchSource<S>, fetch: (s: S) => Promise<T>): Load<T> {
  const data = ref<T | null>(null) as Ref<T | null>;
  const error = ref('');
  const loading = ref(false);
  let current: S;
  let seq = 0;

  async function run(s: S) {
    current = s;
    const mine = ++seq;
    loading.value = true;
    error.value = '';
    try {
      const result = await fetch(s);
      if (mine === seq) data.value = result;
    } catch (e) {
      if (mine === seq) {
        data.value = null;
        error.value = e instanceof Error ? e.message : String(e);
      }
    } finally {
      if (mine === seq) loading.value = false;
    }
  }

  // A route param turns empty while the router leaves the page, before the view
  // unmounts; that is no request to make. Tuple sources are new arrays on every change,
  // so the old data is always dropped when the environment or ID changes.
  watch(
    source,
    (s) => {
      if (Array.isArray(s) ? s.some((part) => !part) : !s) return;
      data.value = null;
      void run(s);
    },
    { immediate: true },
  );

  return { data, error, loading, reload: () => run(current) };
}
