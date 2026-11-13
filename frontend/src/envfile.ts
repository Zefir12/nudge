// envfile reads and writes .env text for the form editor without losing anything:
// parseEnv then serializeEnv gives the text back byte for byte, and a variable whose
// value didn't change keeps its line as written (quotes, export, inline comment).

export type Quote = '' | '"' | "'";

// EnvVar is one KEY=value line and the # comment lines directly above it.
export interface EnvVar {
  kind: 'var';
  name: string;
  value: string;
  // comments are the comment lines above, as written.
  comments: string[];
  // line is the variable's line as written; it is kept while value is unchanged.
  line: string;
  // original is the value the line holds.
  original: string;
  quote: Quote;
  exported: boolean;
  // trailing is an unquoted line's inline comment (" # ..."), kept on a new value.
  trailing: string;
}

// EnvText is lines that belong to no variable: blank lines, comments followed by a blank
// line, and lines that aren't KEY=value (kept as written, not editable).
export interface EnvText {
  kind: 'text';
  lines: string[];
}

export type EnvItem = EnvVar | EnvText;

export interface EnvDoc {
  items: EnvItem[];
  // newline says the text ended with a newline.
  newline: boolean;
}

const lineRe = /^(\s*)(export\s+)?([A-Za-z_][A-Za-z0-9_.-]*)\s*=\s*(.*)$/;

// parseLine reads a KEY=value line; null for anything else, including a quoted value
// that doesn't close on its line (a multi-line value stays text, as written).
export function parseLine(
  line: string,
): Pick<EnvVar, 'name' | 'value' | 'quote' | 'exported' | 'trailing'> | null {
  const m = lineRe.exec(line);
  if (!m) return null;
  const exported = !!m[2];
  const name = m[3]!;
  const rest = m[4]!;
  const q = rest[0];
  if (q === "'") {
    const end = rest.indexOf("'", 1);
    if (end < 0 || !onlyComment(rest.slice(end + 1))) return null;
    return { name, value: rest.slice(1, end), quote: "'", exported, trailing: rest.slice(end + 1) };
  }
  if (q === '"') {
    let value = '';
    for (let i = 1; i < rest.length; i++) {
      const c = rest[i]!;
      if (c === '"') {
        if (!onlyComment(rest.slice(i + 1))) return null;
        return { name, value, quote: '"', exported, trailing: rest.slice(i + 1) };
      }
      if (c === '\\' && i + 1 < rest.length) {
        const n = rest[++i]!;
        value += unescapes[n] ?? `\\${n}`;
      } else {
        value += c;
      }
    }
    return null;
  }
  // Unquoted: an inline comment starts at whitespace followed by #.
  const hash = /\s+#/.exec(rest);
  const value = hash ? rest.slice(0, hash.index) : rest;
  return { name, value: value.trimEnd(), quote: '', exported, trailing: rest.slice(value.trimEnd().length) };
}

const unescapes: Record<string, string> = { n: '\n', t: '\t', r: '\r', '"': '"', '\\': '\\' };

function onlyComment(s: string): boolean {
  return /^\s*(#.*)?$/.test(s);
}

const isComment = (line: string) => line.trimStart().startsWith('#');

// parseEnv splits .env text into variables (with the comment lines directly above
// them) and the text between them.
export function parseEnv(text: string): EnvDoc {
  const newline = text.endsWith('\n');
  const lines = text === '' ? [] : (newline ? text.slice(0, -1) : text).split('\n');
  const items: EnvItem[] = [];
  let pending: string[] = [];
  const flushText = (extra: string[]) => {
    const all = [...pending, ...extra];
    pending = [];
    if (!all.length) return;
    const last = items[items.length - 1];
    if (last?.kind === 'text') last.lines.push(...all);
    else items.push({ kind: 'text', lines: all });
  };
  for (const line of lines) {
    if (isComment(line)) {
      pending.push(line);
      continue;
    }
    const v = parseLine(line);
    if (!v) {
      flushText([line]);
      continue;
    }
    const comments = pending;
    pending = [];
    items.push({ kind: 'var', ...v, comments, line, original: v.value });
  }
  flushText([]);
  return { items, newline };
}

// formatLine writes a variable's line for a new value, in its quoting where the value
// allows it.
export function formatLine(
  v: Pick<EnvVar, 'name' | 'quote' | 'exported' | 'trailing'>,
  value: string,
): string {
  const prefix = `${v.exported ? 'export ' : ''}${v.name}=`;
  let quote = v.quote;
  if (quote === "'" && /['\n\r]/.test(value)) quote = '"';
  if (quote === '' && value !== '' && !/^[^\s#'"\\]+$/.test(value)) quote = '"';
  // An empty unquoted value would take its inline comment as the value.
  if (quote === '' && value === '' && v.trailing.includes('#')) quote = '"';
  const trailing = v.trailing;
  if (quote === "'") return `${prefix}'${value}'${trailing}`;
  if (quote === '"') {
    const escaped = value.replace(/[\\"\n\r\t]/g, (c) => escapes[c]!);
    return `${prefix}"${escaped}"${trailing}`;
  }
  return `${prefix}${value}${trailing}`;
}

const escapes: Record<string, string> = { '\\': '\\\\', '"': '\\"', '\n': '\\n', '\r': '\\r', '\t': '\\t' };

// varLines is a variable's comments and line, its line rewritten if the value changed.
export function varLines(v: EnvVar): string[] {
  return [...v.comments, v.value === v.original ? v.line : formatLine(v, v.value)];
}

// serializeEnv writes a document back to text.
export function serializeEnv(doc: EnvDoc): string {
  const lines = doc.items.flatMap((it) => (it.kind === 'text' ? it.lines : varLines(it)));
  if (!lines.length) return '';
  return lines.join('\n') + (doc.newline ? '\n' : '');
}

// commentText is a comment line as people read it, without the # and one space.
export const commentText = (line: string) => line.trimStart().replace(/^# ?/, '');

const secretRe = /PASS|SECRET|TOKEN|KEY|PRIVATE|CREDENTIAL/i;

// isSecretName says a variable's name looks like it holds a secret.
export const isSecretName = (name: string) => secretRe.test(name);

// EnvForm is what the form editor shows: fields in order, then the variables of the
// current .env that the example lacks.
export interface EnvForm {
  fields: EnvVar[];
  others: EnvVar[];
  // build writes the .env text with the fields' and others' current values.
  build: () => string;
}

const clone = (v: EnvVar): EnvVar => ({ ...v, comments: [...v.comments] });

// formFromEnv is the form for a .env without an example: its own variables, in place,
// with its other lines kept where they are.
export function formFromEnv(env: string): EnvForm {
  const doc = parseEnv(env);
  const fields = doc.items.filter((it): it is EnvVar => it.kind === 'var');
  return { fields, others: [], build: () => serializeEnv(doc) };
}

// formFromExample is the form for a .env with an example from the repository: the
// example's variables in its order and with its comments, valued from the current .env
// (its last line for a name) or else the example; then the current .env's own variables
// and lines that aren't the example's (a variable's or a block's lines the example has
// as written are not repeated).
export function formFromExample(env: string, example: string): EnvForm {
  const ex = parseEnv(example);
  const cur = parseEnv(env);
  const current = new Map<string, EnvVar>();
  for (const it of cur.items) if (it.kind === 'var') current.set(it.name, it);

  const fields: EnvVar[] = [];
  const top: EnvItem[] = ex.items.map((it) => {
    if (it.kind === 'text') return { kind: 'text', lines: [...it.lines] };
    const have = current.get(it.name);
    // The current line keeps its formatting; the example's comments are shown.
    const f = have ? { ...clone(have), comments: [...it.comments] } : clone(it);
    fields.push(f);
    return f;
  });

  const exampleNames = new Set(fields.map((f) => f.name));
  const exampleLines = new Set(ex.items.flatMap((it) => (it.kind === 'text' ? it.lines : it.comments)));
  const others: EnvVar[] = [];
  const rest: EnvItem[] = [];
  for (const it of cur.items) {
    if (it.kind === 'var') {
      if (exampleNames.has(it.name)) {
        // Comments of its own beyond the example's stay, as a block.
        const own = it.comments.filter((l) => !exampleLines.has(l));
        if (own.length) rest.push({ kind: 'text', lines: own });
        continue;
      }
      const o = clone(it);
      others.push(o);
      rest.push(o);
      continue;
    }
    const lines = it.lines.filter((l) => l.trim() !== '' && !exampleLines.has(l));
    if (lines.length) rest.push({ kind: 'text', lines });
  }

  return {
    fields,
    others,
    build: () => {
      let text = serializeEnv({ items: top, newline: true });
      if (text === '\n') text = '';
      const tail = serializeEnv({ items: rest, newline: true });
      if (tail) text += (text ? '\n' : '') + tail;
      return text;
    },
  };
}
