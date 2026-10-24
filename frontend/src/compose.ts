import { isMap, isScalar, parseDocument } from 'yaml';

// ComposeFile is what the browser can tell about a compose file without asking compose.
export interface ComposeFile {
  // errors are YAML syntax errors and a wrong overall shape, one line each.
  errors: string[];
  // services are the names under services:, in file order.
  services: string[];
}

// readCompose parses a compose file for instant feedback in the editor; compose itself
// (checkStack) has the last word on what it accepts.
export function readCompose(text: string): ComposeFile {
  const doc = parseDocument(text);
  // The yaml library's messages end with a snippet of the source after a blank line.
  const errors = doc.errors.map((e) => e.message.split('\n')[0]!);
  if (errors.length) return { errors, services: [] };
  const top = doc.contents;
  if (top === null || (isScalar(top) && top.value === null)) {
    return { errors: ['The file is empty.'], services: [] };
  }
  if (!isMap(top)) return { errors: ['The file must be a mapping with a services: key.'], services: [] };
  const services = top.get('services', true);
  if (services === undefined || (isScalar(services) && services.value === null)) {
    return { errors: [], services: [] };
  }
  if (!isMap(services)) return { errors: ['services: must be a mapping of service names.'], services: [] };
  return {
    errors: [],
    services: services.items.map((p) => String(isScalar(p.key) ? p.key.value : p.key)),
  };
}
