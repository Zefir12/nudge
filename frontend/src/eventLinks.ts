import type { DockerEvent } from './api/client';
import { shortId } from './format';

// Only containers, images, volumes and networks have pages to link to.
const pages: Record<string, string> = {
  container: 'container',
  image: 'image',
  volume: 'volume',
  network: 'network',
};

// eventPage is the route name of the object an event is about, or undefined when it has
// no page or the event removed it.
export function eventPage(e: DockerEvent): string | undefined {
  if (e.action === 'destroy' || e.action === 'delete' || e.action === 'untag') return undefined;
  return pages[e.type];
}

// eventLabel names an event's object: its name, else a short ID.
export const eventLabel = (e: DockerEvent) =>
  e.name || (e.type === 'container' || e.type === 'network' ? shortId(e.id) : e.id);
