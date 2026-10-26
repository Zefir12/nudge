import { onScopeDispose, ref, watch, type Ref } from 'vue';

import { streamEvents, type DockerEvent } from '../api/client';

// Reconnect delays after a stream ends: doubling from 1 s up to 30 s, reset by an event.
const firstDelay = 1000;
const maxDelay = 30_000;

// useEvents follows env's Docker events of the given types (all when empty) for as long
// as the calling component lives, reopening the stream when it ends and when env changes.
// error holds why the last stream ended while a reconnect is pending, else ''. onReconnect
// runs when a stream is reopened after one ended, since events may have been missed.
export function useEvents(
  env: Readonly<Ref<string>>,
  types: string[],
  onEvent: (e: DockerEvent) => void,
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

  function open(e: string) {
    close = streamEvents(e, types, {
      onEvent: (ev) => {
        delay = firstDelay;
        error.value = '';
        onEvent(ev);
      },
      onEnd: (msg) => {
        close = null;
        error.value = msg ?? 'The event stream ended.';
        timer = setTimeout(() => {
          open(e);
          onReconnect?.();
        }, delay);
        delay = Math.min(delay * 2, maxDelay);
      },
    });
  }

  watch(
    env,
    (e) => {
      stop();
      error.value = '';
      delay = firstDelay;
      if (e) open(e);
    },
    { immediate: true },
  );
  onScopeDispose(stop);
  return { error };
}

// useLiveReload calls reload shortly after env's events of the given types, once for a
// burst (a compose up or a prune sends many), and after a reconnect. Exec events change
// no list and are skipped.
export function useLiveReload(
  env: Readonly<Ref<string>>,
  types: string[],
  reload: () => unknown,
  wait = 300,
) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  onScopeDispose(() => clearTimeout(timer));
  const later = () => {
    clearTimeout(timer);
    timer = setTimeout(() => void reload(), wait);
  };
  return useEvents(
    env,
    types,
    (e) => {
      if (!e.action.startsWith('exec_')) later();
    },
    later,
  );
}
