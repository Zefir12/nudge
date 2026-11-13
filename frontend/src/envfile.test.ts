import { describe, expect, it } from 'vite-plus/test';

import {
  type EnvVar,
  commentText,
  formFromEnv,
  formFromExample,
  formatLine,
  isSecretName,
  parseEnv,
  parseLine,
  serializeEnv,
} from './envfile';

const vars = (text: string) => parseEnv(text).items.filter((it): it is EnvVar => it.kind === 'var');

describe('parseLine', () => {
  it.each([
    ['A=1', 'A', '1', '', false, ''],
    ['  A = 1  ', 'A', '1', '', false, '  '],
    ['export A=1', 'A', '1', '', true, ''],
    ['A=', 'A', '', '', false, ''],
    ['A=foo bar', 'A', 'foo bar', '', false, ''],
    ['A=foo # note', 'A', 'foo', '', false, ' # note'],
    ['A=foo#bar', 'A', 'foo#bar', '', false, ''],
    ["A='it is $x'", 'A', 'it is $x', "'", false, ''],
    ['A="a \\"b\\" \\\\ \\n c"', 'A', 'a "b" \\ \n c', '"', false, ''],
    ['A="x" # note', 'A', 'x', '"', false, ' # note'],
    ['A=""', 'A', '', '"', false, ''],
    ['A="\\q"', 'A', '\\q', '"', false, ''],
    ['my.var-1=x', 'my.var-1', 'x', '', false, ''],
  ])('reads %s', (line, name, value, quote, exported, trailing) => {
    expect(parseLine(line)).toEqual({ name, value, quote, exported, trailing });
  });

  it.each(['', '# A=1', 'just text', 'A', '1A=x', 'A="open', "A='open", 'A="x" y'])('refuses %j', (line) => {
    expect(parseLine(line)).toBeNull();
  });
});

describe('parseEnv', () => {
  it('ties comment lines directly above a variable to it', () => {
    const doc = parseEnv('# header\n\n# the port\n# (http)\nPORT=80\nHOST=x\n# dangling\n');
    expect(doc.items).toMatchObject([
      { kind: 'text', lines: ['# header', ''] },
      { kind: 'var', name: 'PORT', comments: ['# the port', '# (http)'] },
      { kind: 'var', name: 'HOST', comments: [] },
      { kind: 'text', lines: ['# dangling'] },
    ]);
  });

  it.each([
    '',
    '\n',
    'A=1',
    'A=1\n',
    '# only a comment',
    '\n\n# c\n\nA=1\n\n\n',
    '# header\n\n# the port\nPORT=80 # inline\nexport HOST="a b"\nKEY=\'s3cr3t\'\n',
    'A="multi\nline"\nB=2\n',
    'garbage line\n  # indented comment\n  A = 1  \r\n',
    'A=1\nA=2\n',
  ])('round-trips %j exactly', (text) => {
    expect(serializeEnv(parseEnv(text))).toBe(text);
    expect(formFromEnv(text).build()).toBe(text);
  });

  it('keeps a multi-line value as text, not as a field', () => {
    expect(vars('A="multi\nline"\nB=2\n').map((v) => v.name)).toEqual(['B']);
  });
});

describe('formatLine', () => {
  const base = { name: 'A', quote: '' as const, exported: false, trailing: '' };
  it.each([
    [base, 'plain', 'A=plain'],
    [base, '', 'A='],
    [base, 'has space', 'A="has space"'],
    [base, 'a#b', 'A="a#b"'],
    [base, 'say "hi"\\', 'A="say \\"hi\\"\\\\"'],
    [base, 'two\nlines', 'A="two\\nlines"'],
    [{ ...base, exported: true }, 'x', 'export A=x'],
    [{ ...base, trailing: ' # note' }, 'x', 'A=x # note'],
    [{ ...base, trailing: ' # note' }, '', 'A="" # note'],
    [{ ...base, quote: "'" as const }, 'a $b', "A='a $b'"],
    [{ ...base, quote: "'" as const }, "it's", 'A="it\'s"'],
    [{ ...base, quote: '"' as const }, 'x', 'A="x"'],
  ])('writes %j with %j', (v, value, want) => {
    expect(formatLine(v, value)).toBe(want);
  });

  it.each(['plain', '', 'a b', ' lead', 'a#b', 'q"q', "s'q", 'back\\slash', 'two\nlines', 'tab\there', '$X'])(
    'reads back %j as written',
    (value) => {
      for (const quote of ['', '"', "'"] as const) {
        for (const trailing of ['', ' # note']) {
          const line = formatLine({ name: 'A', quote, exported: false, trailing }, value);
          expect(parseLine(line)?.value, line).toBe(value);
        }
      }
    },
  );
});

describe('formFromEnv', () => {
  it('rewrites only the changed variable, in place', () => {
    const form = formFromEnv('# c\nA=1 # one\n\nB="x"\n');
    expect(form.fields.map((f) => f.name)).toEqual(['A', 'B']);
    form.fields[0]!.value = 'two words';
    expect(form.build()).toBe('# c\nA="two words" # one\n\nB="x"\n');
  });
});

describe('formFromExample', () => {
  const example = '# App settings\n\n# the port\nPORT=8080\n# db password\nDB_PASSWORD=\nDEBUG=false\n';

  it('takes order and comments from the example and values from .env', () => {
    const form = formFromExample("DEBUG=true\nPORT='80'\n", example);
    expect(form.fields.map((f) => [f.name, f.value, f.comments])).toEqual([
      ['PORT', '80', ['# the port']],
      ['DB_PASSWORD', '', ['# db password']],
      ['DEBUG', 'true', []],
    ]);
    expect(form.others).toEqual([]);
    expect(form.build()).toBe(
      "# App settings\n\n# the port\nPORT='80'\n# db password\nDB_PASSWORD=\nDEBUG=true\n",
    );
  });

  it('keeps variables and lines the example lacks at the bottom', () => {
    const env =
      '# mine\nEXTRA=1\n# about port\n# the port\nPORT=80\nOTHER="a b"\n\n# loose note\nnot a var\n';
    const form = formFromExample(env, example);
    expect(form.others.map((o) => [o.name, o.value, o.comments])).toEqual([
      ['EXTRA', '1', ['# mine']],
      ['OTHER', 'a b', []],
    ]);
    form.others[0]!.value = '2';
    expect(form.build()).toBe(
      '# App settings\n\n# the port\nPORT=80\n# db password\nDB_PASSWORD=\nDEBUG=false\n\n' +
        '# mine\nEXTRA=2\n# about port\nOTHER="a b"\n# loose note\nnot a var\n',
    );
  });

  it('is stable: building from what it built gives the same text', () => {
    const env = 'SECRET=keep\nPORT=80\n# loose\n';
    const once = formFromExample(env, example).build();
    expect(formFromExample(once, example).build()).toBe(once);
    // Nothing of the .env is lost.
    for (const line of env.trim().split('\n')) expect(once.split('\n')).toContain(line);
  });

  it('writes the example alone for an empty .env and the others alone for an empty example', () => {
    expect(formFromExample('', example).build()).toBe(example);
    expect(formFromExample('A=1\n', '').build()).toBe('A=1\n');
    expect(formFromExample('', '').build()).toBe('');
  });
});

describe('helpers', () => {
  it.each([
    ['DB_PASSWORD', true],
    ['db_pass', true],
    ['API_KEY', true],
    ['GITHUB_TOKEN', true],
    ['client_secret', true],
    ['PRIVATE_URL', true],
    ['AWS_CREDENTIALS', true],
    ['PORT', false],
    ['HOST', false],
  ])('isSecretName(%s) = %s', (name, want) => {
    expect(isSecretName(name)).toBe(want);
  });

  it.each([
    ['# a note', 'a note'],
    ['#a note', 'a note'],
    ['  #  spaced', ' spaced'],
  ])('commentText(%j) = %j', (line, want) => {
    expect(commentText(line)).toBe(want);
  });
});
