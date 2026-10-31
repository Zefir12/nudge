// Syntax highlighting for the stack editor: small line-based tokenizers for compose YAML
// and .env files. They only colour text, so they are forgiving: anything they don't
// recognise stays plain, and the YAML parser (compose.ts) still reports real errors.

export type TokenKind =
  | 'key'
  | 'string'
  | 'number'
  | 'literal'
  | 'comment'
  | 'punct'
  | 'anchor'
  | 'variable'
  | '';

export interface Token {
  text: string;
  kind: TokenKind;
}

// push appends text, merging it into the previous token when the kind is the same.
function push(out: Token[], text: string, kind: TokenKind): void {
  if (!text) return;
  const last = out.at(-1);
  if (last && last.kind === kind) last.text += text;
  else out.push({ text, kind });
}

// withVars pushes text as kind, with ${VAR} and $VAR references marked as variables.
function withVars(out: Token[], text: string, kind: TokenKind): void {
  let at = 0;
  for (const m of text.matchAll(/\$\{[^}\n]*\}?|\$[A-Za-z_][A-Za-z0-9_]*/g)) {
    push(out, text.slice(at, m.index), kind);
    push(out, m[0], 'variable');
    at = m.index + m[0].length;
  }
  push(out, text.slice(at), kind);
}

const yamlNumber = /^[-+]?(?:\d[\d_]*(?:\.\d*)?(?:[eE][-+]?\d+)?|\.\d+|0x[\da-fA-F]+|0o[0-7]+|\.inf|\.nan)$/i;
const yamlLiteral = /^(?:true|false|yes|no|on|off|null|~)$/i;

// scalar pushes a plain (unquoted) scalar, coloured by what YAML would read it as.
function scalar(out: Token[], text: string): void {
  const lead = /^\s*/.exec(text)![0];
  const word = text.slice(lead.length).trimEnd();
  const trail = text.slice(lead.length + word.length);
  push(out, lead, '');
  if (yamlNumber.test(word)) push(out, word, 'number');
  else if (yamlLiteral.test(word)) push(out, word, 'literal');
  else withVars(out, word, 'string');
  push(out, trail, '');
}

// yamlValue colours what follows a key or list marker. It returns true when the value
// opens a block scalar (| or >), whose more-indented lines are all string content.
function yamlValue(out: Token[], rest: string): boolean {
  const ws = /^\s*/.exec(rest)![0];
  push(out, ws, '');
  rest = rest.slice(ws.length);
  let m: RegExpExecArray | null;
  while ((m = /^([&*!][^\s,[\]{}]*)(\s*)/.exec(rest))) {
    push(out, m[1]!, 'anchor');
    push(out, m[2]!, '');
    rest = rest.slice(m[0].length);
  }
  if (rest.startsWith('#')) {
    push(out, rest, 'comment');
    return false;
  }
  if ((m = /^([|>][-+0-9]*)(\s*)(#.*)?$/.exec(rest))) {
    push(out, m[1]!, 'punct');
    push(out, m[2]!, '');
    push(out, m[3] ?? '', 'comment');
    return true;
  }
  if ((m = /^"(?:[^"\\]|\\.)*"?|^'(?:[^']|'')*'?/.exec(rest))) {
    withVars(out, m[0], 'string');
    rest = rest.slice(m[0].length);
    const c = rest.search(/\s#/);
    push(out, c < 0 ? rest : rest.slice(0, c), '');
    if (c >= 0) push(out, rest.slice(c), 'comment');
    return false;
  }
  // A comment starts at the first # after whitespace; quotes inside flow
  // collections are rare enough in compose files to ignore here.
  const c = rest.search(/\s#/);
  const text = c < 0 ? rest : rest.slice(0, c);
  if (/^[[{]/.test(text)) {
    for (const part of text.split(/([[\]{},:])/)) {
      if (/^[[\]{},:]$/.test(part)) push(out, part, 'punct');
      else scalar(out, part);
    }
  } else {
    scalar(out, text);
  }
  if (c >= 0) push(out, rest.slice(c), 'comment');
  return false;
}

const yamlKey = /^("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^\s#"'?:,[\]{}&*!|>%@`][^#]*?)(\s*)(:)(?=\s|$)/;

// highlightYaml splits a YAML document into coloured tokens. Joining the token texts
// gives back the input unchanged.
export function highlightYaml(src: string): Token[] {
  const out: Token[] = [];
  // Column a block scalar's lines must be indented past (the key's, after any list
  // dashes), -1 outside one.
  let block = -1;
  src.split('\n').forEach((line, i) => {
    if (i) push(out, '\n', '');
    const indent = line.length - line.trimStart().length;
    if (block >= 0) {
      if (line.trim() === '' || indent > block) {
        push(out, line, 'string');
        return;
      }
      block = -1;
    }
    push(out, line.slice(0, indent), '');
    let rest = line.slice(indent);
    if (rest.startsWith('#')) {
      push(out, rest, 'comment');
      return;
    }
    let m: RegExpExecArray | null;
    if ((m = /^(?:---|\.\.\.)(?=\s|$)/.exec(rest))) {
      push(out, m[0], 'punct');
      rest = rest.slice(m[0].length);
    }
    while ((m = /^-(\s+|$)/.exec(rest))) {
      push(out, '-', 'punct');
      push(out, m[1]!, '');
      rest = rest.slice(m[0].length);
    }
    let col = indent;
    if ((m = yamlKey.exec(rest))) {
      col = line.length - rest.length;
      push(out, m[1]!, 'key');
      push(out, m[2]!, '');
      push(out, m[3]!, 'punct');
      rest = rest.slice(m[0].length);
    }
    if (yamlValue(out, rest)) block = col;
  });
  return out;
}

// highlightEnv splits a .env file into coloured tokens: comments, KEY=value lines (with
// an optional export prefix) and variable references in values. Joining the token
// texts gives back the input unchanged.
export function highlightEnv(src: string): Token[] {
  const out: Token[] = [];
  src.split('\n').forEach((line, i) => {
    if (i) push(out, '\n', '');
    const ws = /^\s*/.exec(line)![0];
    push(out, ws, '');
    const rest = line.slice(ws.length);
    if (rest.startsWith('#')) {
      push(out, rest, 'comment');
      return;
    }
    const m = /^(export\s+)?([^=\s]+)(\s*)(=)(.*)$/.exec(rest);
    if (!m) {
      push(out, rest, '');
      return;
    }
    push(out, m[1] ?? '', 'literal');
    push(out, m[2]!, 'key');
    push(out, m[3]!, '');
    push(out, m[4]!, 'punct');
    const value = m[5]!;
    // Unquoted values end at an inline comment; quoted ones keep their # signs.
    const c = /^\s*["']/.test(value) ? -1 : value.search(/\s#/);
    withVars(out, c < 0 ? value : value.slice(0, c), 'string');
    if (c >= 0) push(out, value.slice(c), 'comment');
  });
  return out;
}
