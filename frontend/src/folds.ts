import { reactive } from 'vue';

const storageKey = 'nudge.foldedCards';

// load reads the card IDs folded in this browser; storage may be unavailable or hold junk.
function load(): string[] {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(storageKey) ?? '[]');
    return Array.isArray(stored) ? stored.filter((id) => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

// IDs come from the code, not from data, so this set stays small. An ID names a kind of
// card ("deployment.runs"), which folds it on every page that shows one.
const folded = reactive(new Set(load()));

export const isFolded = (id: string) => folded.has(id);

export function setFolded(id: string, value: boolean) {
  if (value) folded.add(id);
  else folded.delete(id);
  try {
    localStorage.setItem(storageKey, JSON.stringify([...folded]));
  } catch {
    // Private mode or blocked storage: the card still folds, it just isn't remembered.
  }
}
