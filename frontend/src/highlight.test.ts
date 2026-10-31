import { describe, expect, it } from 'vite-plus/test';

import { highlightEnv, highlightYaml, type Token, type TokenKind } from './highlight';

// kinds lists the coloured tokens as "kind:text", leaving out plain text.
const kinds = (tokens: Token[]) => tokens.filter((t) => t.kind).map((t) => `${t.kind}:${t.text}`);
const of = (tokens: Token[], kind: TokenKind) => tokens.filter((t) => t.kind === kind).map((t) => t.text);

describe('highlightYaml', () => {
  it.each([
    ['key and plain value', 'image: nginx:1.27', ['key:image', 'punct::', 'string:nginx:1.27']],
    ['number', '  replicas: 3', ['key:replicas', 'punct::', 'number:3']],
    ['literal', 'tty: true', ['key:tty', 'punct::', 'literal:true']],
    ['quoted value', 'cmd: "a # b"', ['key:cmd', 'punct::', 'string:"a # b"']],
    ['quoted key', '"x y": 1', ['key:"x y"', 'punct::', 'number:1']],
    ['inline comment', 'a: b # note', ['key:a', 'punct::', 'string:b', 'comment: # note']],
    ['full-line comment', '  # web: {}', ['comment:# web: {}']],
    ['list item', '  - 80:80', ['punct:-', 'string:80:80']],
    ['list of maps', '- name: x', ['punct:-', 'key:name', 'punct::', 'string:x']],
    [
      'anchor and alias',
      'a: &base\nb: *base',
      ['key:a', 'punct::', 'anchor:&base', 'key:b', 'punct::', 'anchor:*base'],
    ],
    [
      'flow sequence',
      'p: [80, 443]',
      ['key:p', 'punct::', 'punct:[', 'number:80', 'punct:,', 'number:443', 'punct:]'],
    ],
    [
      'variable',
      'image: app:${TAG:-latest}',
      ['key:image', 'punct::', 'string:app:', 'variable:${TAG:-latest}'],
    ],
    ['document marker', '---', ['punct:---']],
    ['url value is not a key', 'url: http://x', ['key:url', 'punct::', 'string:http://x']],
  ])('colours %s', (_, src, want) => {
    expect(kinds(highlightYaml(src))).toEqual(want);
  });

  it('treats a block scalar body as a string until the indent drops', () => {
    const t = highlightYaml('cmd: |\n  echo # hi\n  key: no\nnext: 1');
    expect(of(t, 'string')).toEqual(['  echo # hi', '  key: no']);
    expect(of(t, 'key')).toEqual(['cmd', 'next']);
    expect(of(t, 'comment')).toEqual([]);
  });

  it('ends a block scalar under a list dash at the key column', () => {
    const t = highlightYaml('- cmd: |\n    echo\n  env: x');
    expect(of(t, 'string')).toEqual(['    echo', 'x']);
    expect(of(t, 'key')).toEqual(['cmd', 'env']);
  });

  it.each(['', 'services:\n  web:\n    image: x\n', 'a: [b, {c: d}]\n\n# end\n', 'x: "unterminated\n- - y'])(
    'gives back the input %j unchanged',
    (src) => {
      expect(
        highlightYaml(src)
          .map((t) => t.text)
          .join(''),
      ).toBe(src);
    },
  );
});

describe('highlightEnv', () => {
  it.each([
    ['assignment', 'TAG=1.2', ['key:TAG', 'punct:=', 'string:1.2']],
    ['comment', '# secrets below', ['comment:# secrets below']],
    ['export prefix', 'export A=b', ['literal:export ', 'key:A', 'punct:=', 'string:b']],
    ['inline comment', 'A=b # why', ['key:A', 'punct:=', 'string:b', 'comment: # why']],
    ['quoted hash', 'A="b # c"', ['key:A', 'punct:=', 'string:"b # c"']],
    [
      'reference',
      'URL=http://${HOST}/x',
      ['key:URL', 'punct:=', 'string:http://', 'variable:${HOST}', 'string:/x'],
    ],
    ['empty value', 'A=', ['key:A', 'punct:=']],
    ['not an assignment', 'just text', []],
  ])('colours %s', (_, src, want) => {
    expect(kinds(highlightEnv(src))).toEqual(want);
  });

  it('gives back the input unchanged', () => {
    const src = '# c\nA=1\n\n  B = "x"\nnope\n';
    expect(
      highlightEnv(src)
        .map((t) => t.text)
        .join(''),
    ).toBe(src);
  });
});
