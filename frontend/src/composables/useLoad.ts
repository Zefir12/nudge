import { ref, watch, type Ref, type WatchSource } from 'vue';

export interface Load<T> {
  data: Ref<T | null>;
  error: Ref<string>;
  loading: Ref<boolean>;
  reload: () => Promise<void>;
}

// useLoad runs fetch now and whenever source changes (the route's environment or ID), and
// drops answers that arrive after a newer request started.
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

  watch(
    source,
    (s, old) => {
      if (s !== old) data.value = null;
      void run(s);
    },
    { immediate: true },
  );

  return { data, error, loading, reload: () => run(current) };
}
