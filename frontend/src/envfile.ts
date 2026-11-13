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
// line, and lines that aren't KEY=value (kept as written, not editable). raw marks the
// lines of a quoted value that never closes (or closes with junk after it): they are kept
// together and as written, blank lines included.
export interface EnvText {
  kind: 'text';
  lines: string[];
  raw?: boolean;
}

export type EnvItem = EnvVar | EnvText;

export interface EnvDoc {
  items: EnvItem[];
  // newline says the text ended with a newline.
  newline: boolean;
}

// lineRe reads KEY=value; s lets a quoted value run over several lines.
const lineRe = /^([ \t]*)(export[ \t]+)?([A-Za-z_][A-Za-z0-9_.-]*)[ \t]*=(.*)$/s;

type Parsed = Pick<EnvVar, 'name' | 'value' | 'quote' | 'exported' | 'trailing'>;

// parseVar reads KEY=value text without carriage returns; 'open' for a quoted value that
// doesn't close in the text, null for anything else that isn't a variable.
function parseVar(text: string): Parsed | 'open' | null {
  const m = lineRe.exec(text);
  if (!m) return null;
  const exported = !!m[2];
  const name = m[3]!;
  const after = m[4]!;
  const rest = after.trimStart();
  const q = rest[0];
  if (q === "'") {
    const end = rest.indexOf("'", 1);
    if (end < 0) return 'open';
    if (!onlyComment(rest.slice(end + 1))) return null;
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
    return 'open';
  }
  // Unquoted: an inline comment starts at whitespace followed by #, also right after
  // the = ("KEY= # note" is empty).
  const hash = /\s+#/.exec(after);
  const body = hash ? after.slice(0, hash.index) : after;
  return { name, value: body.trim(), quote: '', exported, trailing: after.slice(body.trimEnd().length) };
}

const stripCR = (line: string) => (line.endsWith('\r') ? line.slice(0, -1) : line);

// withCR puts a CRLF line's carriage return back at the end of what follows the value.
function withCR(v: Parsed, last: string): Parsed {
  return last.endsWith('\r') ? { ...v, trailing: v.trailing + '\r' } : v;
}

// parseLine reads a KEY=value line; null for anything else, including a quoted value
// that doesn't close on its line.
export function parseLine(line: string): Parsed | null {
  const v = parseVar(stripCR(line));
  return v && v !== 'open' ? withCR(v, line) : null;
}

const unescapes: Record<string, string> = { n: '\n', t: '\t', r: '\r', '"': '"', '\\': '\\' };

function onlyComment(s: string): boolean {
  return /^\s*(#.*)?$/.test(s);
}

const isComment = (line: string) => line.trimStart().startsWith('#');

// parseEnv splits .env text into variables (with the comment lines directly above
// them) and the text between them. A quoted value may run over several lines; its
// variable's line then holds them all.
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
    if (last?.kind === 'text' && !last.raw) last.lines.push(...all);
    else items.push({ kind: 'text', lines: all });
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    if (isComment(line)) {
      pending.push(line);
      continue;
    }
    const first = parseVar(stripCR(line));
    if (first === 'open') {
      // Take lines until the quote closes; if it never does (or junk follows it), the
      // lines stay together as written.
      let j = i + 1;
      let v: Parsed | 'open' | null = 'open';
      for (; j < lines.length && v === 'open'; j++) {
        v = parseVar(
          lines
            .slice(i, j + 1)
            .map(stripCR)
            .join('\n'),
        );
      }
      const block = lines.slice(i, j);
      i = j - 1;
      if (v && v !== 'open') {
        const p = withCR(v, block[block.length - 1]!);
        const comments = pending;
        pending = [];
        items.push({ kind: 'var', ...p, comments, line: block.join('\n'), original: p.value });
      } else {
        flushText([]);
        items.push({ kind: 'text', lines: block, raw: true });
      }
      continue;
    }
    const v = first && withCR(first, line);
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
  // A single-quoted value is literal. Moved into double quotes, where compose
  // interpolates and reads escapes, its $ becomes $$ and every backslash is escaped,
  // so it keeps its meaning.
  const literal = quote === "'" && /['\n\r]/.test(value);
  if (literal) quote = '"';
  if (quote === '' && value !== '' && !/^[^\s#'"\\]+$/.test(value)) quote = '"';
  // An empty unquoted value would take its inline comment as the value.
  if (quote === '' && value === '' && v.trailing.includes('#')) quote = '"';
  const trailing = v.trailing;
  if (quote === "'") return `${prefix}'${value}'${trailing}`;
  if (quote === '"') {
    const escaped = literal
      ? value.replace(/[\\"\n\r\t]/g, (c) => escapes[c]!).replace(/\$/g, '$$$$')
      : // A backslash that parseVar keeps as written (\$, \q) stays as it is, so
        // rewriting doesn't change it; one that would start an escape is escaped.
        value.replace(/\\(?=[ntr"\\]|$)|["\n\r\t]/g, (c) => escapes[c]!);
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
  // dropped names the variables the example has that the .env sets more than once: the
  // form uses the .env's last line for each and leaves the earlier ones out.
  dropped: string[];
  // build writes the .env text with the fields' and others' current values.
  build: () => string;
}

const clone = (v: EnvVar): EnvVar => ({ ...v, comments: [...v.comments] });

// formFromEnv is the form for a .env without an example: its own variables, in place,
// with its other lines kept where they are.
export function formFromEnv(env: string): EnvForm {
  const doc = parseEnv(env);
  const fields = doc.items.filter((it): it is EnvVar => it.kind === 'var');
  return { fields, others: [], dropped: [], build: () => serializeEnv(doc) };
}

// formFromExample is the form for a .env with an example from the repository: the
// example's variables in its order and with its comments, valued from the current .env
// (its last line for a name, see dropped) or else the example; then the current .env's
// own variables and lines that aren't the example's (a variable's or a block's lines the
// example has as written are not repeated). A name the example sets twice is one field,
// at its first place.
export function formFromExample(env: string, example: string): EnvForm {
  const ex = parseEnv(example);
  const cur = parseEnv(env);
  const current = new Map<string, EnvVar>();
  for (const it of cur.items) if (it.kind === 'var') current.set(it.name, it);

  const fields: EnvVar[] = [];
  const exampleNames = new Set<string>();
  const top: EnvItem[] = ex.items.map((it): EnvItem => {
    if (it.kind === 'text') return { ...it, lines: [...it.lines] };
    // A repeated name keeps only its comments: a second line would override the field.
    if (exampleNames.has(it.name)) return { kind: 'text', lines: [...it.comments] };
    exampleNames.add(it.name);
    const have = current.get(it.name);
    // The current line keeps its formatting; the example's comments are shown.
    const f = have ? { ...clone(have), comments: [...it.comments] } : clone(it);
    fields.push(f);
    return f;
  });

  const exampleLines = new Set(
    ex.items.flatMap((it) => (it.kind === 'text' ? (it.raw ? [] : it.lines) : it.comments)),
  );
  const others: EnvVar[] = [];
  const rest: EnvItem[] = [];
  const dropped = new Set<string>();
  for (const it of cur.items) {
    if (it.kind === 'var') {
      if (exampleNames.has(it.name)) {
        if (current.get(it.name) !== it) dropped.add(it.name);
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
    // An unclosed quoted value's lines are kept whole, as written.
    if (it.raw) {
      rest.push({ ...it, lines: [...it.lines] });
      continue;
    }
    const lines = it.lines.filter((l) => l.trim() !== '' && !exampleLines.has(l));
    if (lines.length) rest.push({ kind: 'text', lines });
  }

  return {
    fields,
    others,
    dropped: [...dropped],
    build: () => {
      let text = serializeEnv({ items: top, newline: true });
      if (text === '\n') text = '';
      const tail = serializeEnv({ items: rest, newline: true });
      if (tail) text += (text ? '\n' : '') + tail;
      return text;
    },
  };
}
