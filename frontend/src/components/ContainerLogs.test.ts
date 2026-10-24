import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubSockets, type FakeWebSocket } from '../test-utils';
import ContainerLogs from './ContainerLogs.vue';

let sockets: FakeWebSocket[];

beforeEach(() => {
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const mountLogs = () => mount(ContainerLogs, { props: { env: '1', id: 'abc 123', name: 'web' } });

const lines = (wrapper: ReturnType<typeof mountLogs>) => wrapper.findAll('.line').map((l) => l.text());

describe('ContainerLogs', () => {
  it('follows the stream with the chosen options', async () => {
    const wrapper = mountLogs();
    expect(sockets).toHaveLength(1);
    const url = new URL(sockets[0]!.url);
    expect(url.protocol).toBe('ws:');
    expect(url.pathname).toBe('/api/v1/environments/1/containers/abc%20123/logs');
    expect(url.searchParams.get('follow')).toBe('true');
    expect(url.searchParams.get('tail')).toBe('500');
    expect(url.searchParams.has('timestamps')).toBe(false);

    await wrapper.find('input[type="checkbox"]').setValue(true);
    await wrapper.find('select').setValue('all');
    await flushPromises();
    expect(sockets[0]!.closed).toBe(true);
    const last = new URL(sockets.at(-1)!.url);
    expect(last.searchParams.get('timestamps')).toBe('true');
    expect(last.searchParams.get('tail')).toBe('all');
  });

  it('shows lines, marks stderr and filters by search', async () => {
    const wrapper = mountLogs();
    const ws = sockets[0]!;
    ws.message({ stream: 'stdout', line: 'starting' });
    ws.message({ stream: 'stderr', time: '2026-10-01T12:00:00Z', line: 'warn: slow' });
    ws.message({ stream: 'stdout', line: 'ready' });
    await flushPromises();
    expect(lines(wrapper)).toEqual(['starting', '2026-10-01T12:00:00Zwarn: slow', 'ready']);
    expect(wrapper.findAll('.line')[1]!.classes()).toContain('stderr');

    await wrapper.find('input[type="search"]').setValue('WARN');
    expect(lines(wrapper)).toEqual(['2026-10-01T12:00:00Zwarn: slow']);
    await wrapper.find('input[type="search"]').setValue('nothing');
    expect(wrapper.text()).toContain('No matching lines.');
  });

  it('pauses and resumes auto-scroll', async () => {
    const wrapper = mountLogs();
    const pause = wrapper.findAll('button').find((b) => b.text() === 'Pause')!;
    await pause.trigger('click');
    expect(wrapper.find('[role="status"]').text()).toBe('Live, paused');
    await pause.trigger('click');
    expect(wrapper.find('[role="status"]').text()).toBe('Live');
  });

  it('downloads the lines', async () => {
    // jsdom has no object URLs; each test file runs in its own environment.
    const create = vi.fn<(b: Blob) => string>(() => 'blob:logs');
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    const wrapper = mountLogs();
    sockets[0]!.message({ stream: 'stdout', time: '2026-10-01T12:00:00Z', line: 'one' });
    sockets[0]!.message({ stream: 'stderr', line: 'two' });
    await flushPromises();
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Download')!
      .trigger('click');
    expect(click).toHaveBeenCalled();
    expect(await create.mock.calls[0]![0].text()).toBe('2026-10-01T12:00:00Z one\ntwo\n');
  });

  it('shows the end of the stream and reconnects', async () => {
    const wrapper = mountLogs();
    sockets[0]!.message({ error: 'the host failed' });
    await flushPromises();
    expect(wrapper.find('[role="status"]').text()).toBe('the host failed');
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Reconnect')!
      .trigger('click');
    expect(sockets).toHaveLength(2);
    sockets[1]!.message({ done: true });
    await flushPromises();
    expect(wrapper.find('[role="status"]').text()).toBe('Stream ended');
  });

  it('closes the socket when unmounted', () => {
    const wrapper = mountLogs();
    wrapper.unmount();
    expect(sockets[0]!.closed).toBe(true);
  });
});
