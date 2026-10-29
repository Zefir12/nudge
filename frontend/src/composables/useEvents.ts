import { onScopeDispose, type Ref } from 'vue';

import { streamEvents, type DockerEvent } from '../api/client';
import { useStream } from './useStream';

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
  return useStream<DockerEvent>(
    env,
    (e, onItem, onEnd) => streamEvents(e, types, { onEvent: onItem, onEnd }),
    onEvent,
    'The event stream ended.',
    onReconnect,
  );
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
