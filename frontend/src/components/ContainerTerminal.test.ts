import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubSockets, type FakeWebSocket } from '../test-utils';
import ContainerTerminal from './ContainerTerminal.vue';

// xterm.js needs a real layout; the fake records what the component does with it.
const terms = vi.hoisted(() => [] as FakeTerminal[]);

interface FakeTerminal {
  cols: number;
  rows: number;
  options: { disableStdin?: boolean };
  written: string[];
  disposed: boolean;
  data?: (d: string) => void;
  resize?: (s: { cols: number; rows: number }) => void;
}

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 100;
    rows = 30;
    options: { disableStdin?: boolean } = {};
    written: string[] = [];
    disposed = false;
    data?: (d: string) => void;
    resize?: (s: { cols: number; rows: number }) => void;
    constructor() {
      terms.push(this);
    }
    loadAddon() {}
    open() {}
    focus() {}
    write(d: Uint8Array) {
      this.written.push(new TextDecoder().decode(d));
    }
    onData(fn: (d: string) => void) {
      this.data = fn;
    }
    onResize(fn: (s: { cols: number; rows: number }) => void) {
      this.resize = fn;
    }
    dispose() {
      this.disposed = true;
    }
  },
}));
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}));
vi.mock('@xterm/xterm/css/xterm.css', () => ({}));

let sockets: FakeWebSocket[];

beforeEach(() => {
  sockets = stubSockets();
  terms.length = 0;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const mountTerminal = () => mount(ContainerTerminal, { props: { env: '1', id: 'abc 123' } });

async function connect(wrapper: ReturnType<typeof mountTerminal>, cmd?: string) {
  if (cmd !== undefined) await wrapper.get('input[aria-label="Command"]').setValue(cmd);
  await wrapper.get('form').trigger('submit');
  await flushPromises();
  return { ws: sockets.at(-1)!, term: terms.at(-1)! };
}

describe('ContainerTerminal', () => {
  it('opens nothing until asked', () => {
    const wrapper = mountTerminal();
    expect(sockets).toHaveLength(0);
    expect(wrapper.text()).toContain('Connect');
  });

  it('runs the command sized to the terminal and pipes both ways', async () => {
    const wrapper = mountTerminal();
    const { ws, term } = await connect(wrapper, 'bash -l');
    const url = new URL(ws.url);
    expect(url.pathname).toBe('/api/v1/environments/1/containers/abc%20123/exec');
    expect(Object.fromEntries(url.searchParams)).toEqual({ cmd: 'bash -l', cols: '100', rows: '30' });
    expect(ws.binaryType).toBe('arraybuffer');
    expect(wrapper.text()).toContain('Connected');

    // Typed before the socket opened, sent once it does.
    term.data!('l');
    expect(ws.sent).toHaveLength(0);
    ws.open();
    term.data!('s\r');
    term.resize!({ cols: 120, rows: 40 });
    expect(ws.sent.map((f) => (typeof f === 'string' ? f : new TextDecoder().decode(f)))).toEqual([
      'l',
      's\r',
      '{"type":"resize","cols":120,"rows":40}',
    ]);

    ws.binary('$ ');
    expect(term.written).toEqual(['$ ']);

    ws.message({ done: true, exit_code: 130 });
    await flushPromises();
    expect(wrapper.text()).toContain('Session ended (exit code 130)');
    expect(term.options.disableStdin).toBe(true);
    expect(wrapper.text()).toContain('Reconnect');
  });

  it('splits a large paste into frames under the server limit', async () => {
    const { ws, term } = await connect(mountTerminal());
    ws.open();
    term.data!('x'.repeat(40000));
    expect(ws.sent.map((f) => (f as Uint8Array).length)).toEqual([16384, 16384, 7232]);
  });

  it('shows an error and a refused session', async () => {
    const wrapper = mountTerminal();
    const { ws } = await connect(wrapper);
    expect(new URL(ws.url).searchParams.get('cmd')).toBe('/bin/sh');
    ws.onclose!();
    await flushPromises();
    expect(wrapper.get('[role="status"]').text()).toBe('The terminal session closed unexpectedly.');
    expect(wrapper.get('[role="status"]').classes()).toContain('error');
  });

  it('closes the session on disconnect and unmount', async () => {
    const wrapper = mountTerminal();
    const first = await connect(wrapper);
    await wrapper.get('button[type="button"]').trigger('click');
    expect(first.ws.closed).toBe(true);
    expect(wrapper.text()).toContain('Disconnected');

    const second = await connect(wrapper);
    expect(first.term.disposed).toBe(true);
    wrapper.unmount();
    expect(second.ws.closed).toBe(true);
    expect(second.term.disposed).toBe(true);
  });
});
