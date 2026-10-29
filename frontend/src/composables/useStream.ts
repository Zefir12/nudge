import { onScopeDispose, ref, watch, type Ref } from 'vue';

// Reconnect delays after a stream ends: doubling from 1 s up to 30 s, reset by an item.
const firstDelay = 1000;
const maxDelay = 30_000;

// StreamOpener opens env's stream and returns a function that closes it; onEnd runs once
// when the stream ends on its own.
export type StreamOpener<T> = (
  env: string,
  onItem: (item: T) => void,
  onEnd: (error?: string) => void,
) => () => void;

// useStream follows env's stream for as long as the calling scope lives, reopening it
// when it ends (after a growing delay) and when env changes; an empty env opens nothing.
// error holds why the last stream ended while a reconnect is pending, else ''.
// onReconnect runs when a stream is reopened after one ended.
export function useStream<T>(
  env: Readonly<Ref<string>>,
  open: StreamOpener<T>,
  onItem: (item: T) => void,
  ended: string,
  onReconnect?: () => void,
) {
  const error = ref('');
  let close: (() => void) | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let delay = firstDelay;

  function stop() {
    clearTimeout(timer);
    close?.();
    close = null;
  }

  function start(e: string) {
    close = open(
      e,
      (item) => {
        delay = firstDelay;
        error.value = '';
        onItem(item);
      },
      (msg) => {
        close = null;
        error.value = msg ?? ended;
        timer = setTimeout(() => {
          start(e);
          onReconnect?.();
        }, delay);
        delay = Math.min(delay * 2, maxDelay);
      },
    );
  }

  watch(
    env,
    (e) => {
      stop();
      error.value = '';
      delay = firstDelay;
      if (e) start(e);
    },
    { immediate: true },
  );
  onScopeDispose(stop);
  return { error };
}
