import { describe, expect, it } from 'vite-plus/test';

import {
  type EnvVar,
  commentText,
  formFromEnv,
  formFromExample,
  formWithHints,
  formatLine,
  isSecret,
  isSecretName,
  parseCommented,
  parseEnv,
  parseLine,
  serializeEnv,
} from './envfile';
import embrExample from './testdata/embr-staging.env.example?raw';

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
    ['A= # note', 'A', '', '', false, ' # note'],
    ['A=   # note', 'A', '', '', false, '   # note'],
    ['A= x', 'A', 'x', '', false, ''],
    ['A=1\r', 'A', '1', '', false, '\r'],
    ['A="x" # n\r', 'A', 'x', '"', false, ' # n\r'],
    ["A='x'\r", 'A', 'x', "'", false, '\r'],
    ['A=\r', 'A', '', '', false, '\r'],
    ['A="\\$x"', 'A', '\\$x', '"', false, ''],
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
    'A=1\r\n# c\r\nB="x"\r\n',
    'K="-----BEGIN KEY-----\nabc\n\n-----END KEY-----"\nB=2\n',
    "K='one\r\ntwo'\r\n",
    'K="never closed\n\nB=2\n',
    'K="closed" junk\nB=2\n',
  ])('round-trips %j exactly', (text) => {
    expect(serializeEnv(parseEnv(text))).toBe(text);
    expect(formFromEnv(text).build()).toBe(text);
  });

  it('reads a quoted value over several lines as one variable', () => {
    const vs = vars('# pem\nK="-----BEGIN-----\nabc\n\n-----END-----" # n\nB=2\n');
    expect(vs.map((v) => [v.name, v.value, v.comments, v.trailing])).toEqual([
      ['K', '-----BEGIN-----\nabc\n\n-----END-----', ['# pem'], ' # n'],
      ['B', '2', [], ''],
    ]);
    expect(vars("K='a\r\nb'\r\n")[0]).toMatchObject({ value: 'a\nb', trailing: '\r' });
  });

  it('keeps an unclosed quoted value as one raw block of text', () => {
    const doc = parseEnv('# c\nK="open\n\nB=2\n');
    expect(doc.items).toEqual([
      { kind: 'text', lines: ['# c'] },
      { kind: 'text', lines: ['K="open', '', 'B=2'], raw: true },
    ]);
  });

  it('reads the variables of a CRLF file', () => {
    expect(vars('A=1\r\n# c\r\nB="x y"\r\n').map((v) => [v.name, v.value, v.comments])).toEqual([
      ['A', '1', []],
      ['B', 'x y', ['# c\r']],
    ]);
    const form = formFromEnv('A=1\r\nB=2\r\n');
    form.fields[0]!.value = 'a b';
    expect(form.build()).toBe('A="a b"\r\nB=2\r\n');
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
    [{ ...base, quote: "'" as const }, "pa$$w'x", 'A="pa$$$$w\'x"'],
    [{ ...base, quote: "'" as const }, "a\\$'", 'A="a\\\\$$\'"'],
    [{ ...base, quote: '"' as const }, 'p\\$x', 'A="p\\$x"'],
    [{ ...base, quote: '"' as const }, 'a\\nb\\', 'A="a\\\\nb\\\\"'],
    [{ ...base, trailing: '\r' }, 'a b', 'A="a b"\r'],
  ])('writes %j with %j', (v, value, want) => {
    expect(formatLine(v, value)).toBe(want);
  });

  it.each([
    'plain',
    '',
    'a b',
    ' lead',
    'a#b',
    'q"q',
    "s'q",
    'back\\slash',
    'two\nlines',
    'tab\there',
    '$X',
    '\\$X',
    '\\\\$X',
    'end\\',
  ])('reads back %j as written', (value) => {
    for (const quote of ['', '"', "'"] as const) {
      for (const trailing of ['', ' # note']) {
        const line = formatLine({ name: 'A', quote, exported: false, trailing }, value);
        expect(parseLine(line)?.value, line).toBe(value);
      }
    }
  });
});

describe('formFromEnv', () => {
  it('keeps an unchanged \\$ escape when a value is rewritten', () => {
    const form = formFromEnv('A="p\\$x"\n');
    expect(form.fields[0]!.value).toBe('p\\$x');
    form.fields[0]!.value += 'y';
    expect(form.build()).toBe('A="p\\$xy"\n');
  });

  it('rewrites only the changed variable, in place', () => {
    const form = formFromEnv('# c\nA=1 # one\n\nB="x"\n');
    expect(form.fields.map((f) => f.name)).toEqual(['A', 'B']);
    form.fields[0]!.value = 'two words';
    expect(form.build()).toBe('# c\nA="two words" # one\n\nB="x"\n');
  });
});

describe('commented-out variables', () => {
  it.each([
    ['# KEY=', 'KEY', ''],
    ['#KEY=value', 'KEY', 'value'],
    ['  #  Photos__S3__Region=garage', 'Photos__S3__Region', 'garage'],
    ['# A="two words"', 'A', 'two words'],
    ['# A=1 # note', 'A', '1'],
  ])('parseCommented(%j) reads %s=%j', (line, name, value) => {
    expect(parseCommented(line)).toMatchObject({ name, value });
  });

  it.each([
    '# Host=<host>;Port=5432;Database=embr_staging;Username=embr_staging;Password=...',
    '#   (`docker inspect <postgres container>`, under Networks), e.g. DB_NETWORK=postgres18_default',
    '# e.g. DB_NETWORK=x',
    '# Note: A=b',
    '# With Enabled=true every value is required.',
    '# MODE=fast is the default',
    '# export A=1',
    '# a.b=1',
    '# A="open',
    '# a note',
  ])('parseCommented(%j) is prose', (line) => {
    expect(parseCommented(line)).toBeNull();
  });

  it('are variables only with the commented option', () => {
    const text = '# about A\n# A=1\nB=2\n';
    expect(vars(text).map((v) => [v.name, v.comments])).toEqual([['B', ['# about A', '# A=1']]]);
    const doc = parseEnv(text, { commented: true });
    const got = doc.items.filter((it): it is EnvVar => it.kind === 'var');
    expect(got.map((v) => [v.name, v.value, v.original, v.commented, v.comments])).toEqual([
      ['A', '', '1', true, ['# about A']],
      ['B', '2', '2', undefined, []],
    ]);
    expect(serializeEnv(doc)).toBe(text);
  });

  it('keeps a .env with commented-out variables byte for byte', () => {
    for (const text of [embrExample, embrExample.replace(/\n/g, '\r\n'), '# A=1\r\n#B=\nC=3']) {
      expect(serializeEnv(parseEnv(text, { commented: true }))).toBe(text);
      expect(formFromEnv(text).build()).toBe(text);
    }
  });

  it("offers a .env's commented-out variables and uncomments those filled in, in place", () => {
    const env =
      '# proxies\n# ForwardedHeaders__KnownProxies__0=\n# the origin\nFrontend__Cors__Origins__0=https://a\n# A=x\r\nA=1\n# B=1\n# B=2\n';
    const form = formFromEnv(env);
    expect(form.fields.map((v) => [v.name, v.comments])).toEqual([
      ['Frontend__Cors__Origins__0', ['# the origin']],
      ['A', []],
    ]);
    expect(form.unset.map((v) => [v.name, v.original, v.comments])).toEqual([
      ['ForwardedHeaders__KnownProxies__0', '', ['# proxies']],
      ['B', '1', []],
    ]);
    form.unset[0]!.value = '10.0.0.1';
    form.unset[1]!.value = '1';
    expect(form.build()).toBe(
      '# proxies\nForwardedHeaders__KnownProxies__0=10.0.0.1\n# the origin\nFrontend__Cors__Origins__0=https://a\n# A=x\r\nA=1\nB=1\n# B=2\n',
    );
  });

  it("offers every key of embr's staging example, commented-out ones included", () => {
    const form = formWithHints('EMBR_IMAGE=x\n', embrExample);
    const names = form.unset.map((v) => v.name);
    expect(names).toEqual([
      'ConnectionStrings__Default',
      'DB_NETWORK',
      'DB_NETWORK_EXTERNAL',
      'Auth__BootstrapAdminEmail',
      'Auth__BootstrapAdminPassword',
      'Auth__BootstrapAdminName',
      'ForwardedHeaders__KnownProxies__0',
      'ForwardedHeaders__KnownNetworks__0',
      'Frontend__Cors__Origins__0',
      'AccountTokens__Enabled',
      'AccountTokens__Key',
      'Oidc__Enabled',
      'Oidc__Authority',
      'Oidc__ClientId',
      'Oidc__ClientSecret',
      'Oidc__PostLogoutRedirectUri',
      'Oidc__AutoCreateUsers',
      'Oidc__MapAdminRole',
      'Oidc__AdminRole',
      'Fitbit__ClientId',
      'Fitbit__ClientSecret',
      'DeepSeek__ApiKey',
      'Photos__Enabled',
      'Photos__S3__ServiceUrl',
      'Photos__S3__Region',
      'Photos__S3__Bucket',
      'Photos__S3__AccessKey',
      'Photos__S3__SecretKey',
      'SupabaseSync__Enabled',
      'SupabaseSync__ConnectionString',
      'SupabaseSync__OwnerEmail',
      'EMBR_PUBLISH',
    ]);
    const by = (n: string) => form.unset.find((v) => v.name === n)!;
    expect(by('Photos__S3__Region').original).toBe('garage');
    expect(by('AccountTokens__Enabled').original).toBe('true');
    expect(by('ConnectionStrings__Default').comments.map(commentText)).toEqual([
      'Shared PostgreSQL instance: own database and user for staging. Npgsql format:',
      'Host=<host>;Port=5432;Database=embr_staging;Username=embr_staging;Password=...',
    ]);
    expect(by('Oidc__Enabled').comments.map(commentText)).toEqual([
      'Optional: login with authestra (OIDC), backend/README.md "Login with authestra".',
      'Redirect URI to register in authestra: https://<staging hostname>/api/auth/callback',
    ]);
    expect(by('Oidc__Authority').comments).toEqual([]);
    expect(by('DB_NETWORK').comments).toHaveLength(6);

    expect(form.build()).toBe('EMBR_IMAGE=x\n');
    by('Photos__S3__Region').value = 'garage';
    by('Oidc__Enabled').value = 'true';
    expect(form.build()).toBe(
      'EMBR_IMAGE=x\n' +
        '# Optional: login with authestra (OIDC), backend/README.md "Login with authestra".\n' +
        '# Redirect URI to register in authestra: https://<staging hostname>/api/auth/callback\n' +
        'Oidc__Enabled=true\n' +
        'Photos__S3__Region=garage\n',
    );
  });

  it("prefers the .env's own commented-out variable over the example's", () => {
    const form = formWithHints('# note\n# A=mine\nB=1\n', '# from example\n# A=theirs\n# B=2\n# C=3\n');
    expect(form.unset.map((v) => [v.name, v.original])).toEqual([
      ['A', 'mine'],
      ['C', '3'],
    ]);
    form.unset[0]!.value = 'v';
    form.unset[1]!.value = 'w';
    expect(form.build()).toBe('# note\nA=v\nB=1\nC=w\n');
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

  it('keeps a multi-line value whole, as one field, when the example names it', () => {
    const pem = 'K="-----BEGIN-----\nabc\n\n# PORT=8080\n-----END-----"';
    const ex = '# the port\nPORT=8080\n# key\nK=\n';
    const form = formFromExample(`${pem}\nPORT=81\n`, ex);
    expect(form.fields.map((f) => [f.name, f.value])).toEqual([
      ['PORT', '81'],
      ['K', '-----BEGIN-----\nabc\n\n# PORT=8080\n-----END-----'],
    ]);
    expect(form.others).toEqual([]);
    const built = form.build();
    expect(built).toBe(`# the port\nPORT=81\n# key\n${pem}\n`);
    expect(built.match(/^K=/gm)).toHaveLength(1);
    expect(formFromExample(built, ex).build()).toBe(built);
  });

  it('keeps an unclosed quoted value whole at the bottom', () => {
    const form = formFromExample('K="open\n\nPORT=8080\n', example);
    expect(form.build()).toBe(
      '# App settings\n\n# the port\nPORT=8080\n# db password\nDB_PASSWORD=\nDEBUG=false\n\n' +
        'K="open\n\nPORT=8080\n',
    );
  });

  it('uses the last of a repeated .env variable and says the earlier ones are dropped', () => {
    const form = formFromExample('PORT=1\nDEBUG=true\nPORT=2\nX=1\nX=2\n', example);
    expect(form.fields.map((f) => [f.name, f.value])).toEqual([
      ['PORT', '2'],
      ['DB_PASSWORD', ''],
      ['DEBUG', 'true'],
    ]);
    expect(form.dropped).toEqual(['PORT']);
    // Repeats of a variable the example lacks stay as written, in order.
    expect(form.others.map((o) => [o.name, o.value])).toEqual([
      ['X', '1'],
      ['X', '2'],
    ]);
    expect(form.build()).toContain('# the port\nPORT=2\n');
    expect(form.build()).not.toContain('PORT=1');
    expect(formFromExample('PORT=1\n', example).dropped).toEqual([]);
  });

  it('makes one field of a name the example repeats', () => {
    const ex = '# first\nA=1\nB=2\n# again\nA=3\n';
    const form = formFromExample('A=9\n', ex);
    expect(form.fields.map((f) => [f.name, f.value, f.comments])).toEqual([
      ['A', '9', ['# first']],
      ['B', '2', []],
    ]);
    expect(form.build()).toBe('# first\nA=9\nB=2\n# again\n');
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

describe('formWithHints', () => {
  const example = '# Section\n\n# the port\nPORT=8080\n# db password\nDB_PASSWORD=changeme\nPORT=9\n';

  it('lists the example variables the .env lacks, empty, with the example value as hint', () => {
    const form = formWithHints('PORT=80\nEXTRA=1\n', example);
    expect(form.fields.map((v) => v.name)).toEqual(['PORT', 'EXTRA']);
    expect(form.unset.map((v) => [v.name, v.value, v.original, v.comments])).toEqual([
      ['DB_PASSWORD', '', 'changeme', ['# db password']],
    ]);
  });

  it('leaves the .env as it is until a hint is filled in', () => {
    expect(formWithHints('PORT=80\n', example).build()).toBe('PORT=80\n');
    expect(formWithHints('', example).build()).toBe('');
  });

  it('appends the variables filled in, with their comments', () => {
    const form = formWithHints('A=1', example);
    form.unset.find((v) => v.name === 'DB_PASSWORD')!.value = 's3 cret';
    expect(form.build()).toBe('A=1\n# db password\nDB_PASSWORD="s3 cret"\n');
    const empty = formWithHints('', example);
    empty.unset[0]!.value = '81';
    expect(empty.build()).toBe('# the port\nPORT=81\n');
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
    ['AccountTokens__Key', true],
    ['Photos__S3__AccessKey', true],
    ['Photos__S3__SecretKey', true],
    ['Oidc__ClientSecret', true],
    ['DeepSeek__ApiKey', true],
    ['APIKEY', true],
    ['SupabaseSync__ConnectionString', true],
    ['ConnectionStrings__Default', true],
    ['Auth__BootstrapAdminPassword', true],
    ['AccountTokens__Enabled', false],
    ['KEYCLOAK_URL', false],
    ['Oidc__ClientId', false],
    ['PORT', false],
    ['HOST', false],
  ])('isSecretName(%s) = %s', (name, want) => {
    expect(isSecretName(name)).toBe(want);
  });

  it.each([
    ['DB_PASSWORD', 's3cret', true],
    ['DB_PASSWORD', '', true],
    ['SECRET_ENABLED', 'true', false],
    ['TOKEN_AUTH', 'False', false],
    ['AccountTokens__Enabled', 'true', false],
    ['SupabaseSync__ConnectionString', 'postgresql://ro:pw@db.supabase.co:5432/postgres', true],
    ['DATABASE_URL', 'postgresql://ro:pw@db:5432/app', true],
    ['DATABASE_URL', 'postgresql://db:5432/app', false],
    ['UPSTREAM', 'https://user@host/', false],
    ['DB', 'Host=h;Port=5432;Password=p', true],
    ['PORT', '80', false],
  ])('isSecret(%s, %j) = %s', (name, value, want) => {
    expect(isSecret(name, value)).toBe(want);
  });

  it.each([
    ['# a note', 'a note'],
    ['#a note', 'a note'],
    ['  #  spaced', ' spaced'],
  ])('commentText(%j) = %j', (line, want) => {
    expect(commentText(line)).toBe(want);
  });
});
