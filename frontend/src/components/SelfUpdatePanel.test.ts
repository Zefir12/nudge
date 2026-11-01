import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import type { SelfUpdate } from '../api/client';
import { page, restarting } from '../reconnect';
import { stubApi } from '../test-utils';
import SelfUpdatePanel from './SelfUpdatePanel.vue';

const state = (over: Partial<SelfUpdate> = {}): SelfUpdate => ({
  version: 'v1.2.0',
  image: 'registry.example/nudge:latest',
  status: 'available',
  local_digest: 'sha256:aaa',
  remote_digest: 'sha256:bbb',
  checked_at: '2026-10-28T12:00:00Z',
  can_update: true,
  ...over,
});

const button = (wrapper: ReturnType<typeof mount>, label: string) =>
  wrapper.findAll('button').find((b) => b.text() === label);

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('SelfUpdatePanel', () => {
  it.each([
    { name: 'server', env: undefined, base: '/system/update' },
    { name: 'agent', env: '2', base: '/environments/2/agent/update' },
  ])('shows and checks the $name', async ({ env, base }) => {
    stubApi({
      [`GET ${base}`]: { body: state({ status: 'unknown', error: 'not checked yet' }) },
      [`POST ${base}/check`]: { body: state() },
    });
    const wrapper = mount(SelfUpdatePanel, { props: { env } });
    await flushPromises();
    expect(wrapper.text()).toContain('v1.2.0');
    expect(wrapper.text()).toContain('not checked yet');
    expect(button(wrapper, 'Update')).toBeUndefined();
    await button(wrapper, 'Check for updates')!.trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('Update available');
    expect(button(wrapper, 'Update')).toBeDefined();
  });

  it('says why it cannot update', async () => {
    stubApi({ 'GET /system/update': { body: state({ can_update: false, reason: 'no socket' }) } });
    const wrapper = mount(SelfUpdatePanel);
    await flushPromises();
    expect(wrapper.text()).toContain('no socket');
    expect(button(wrapper, 'Update')).toBeUndefined();
  });

  it('reports nothing newer', async () => {
    vi.stubGlobal('confirm', () => true);
    stubApi({
      'GET /system/update': { body: state() },
      'POST /system/update': { body: { started: false } },
    });
    const wrapper = mount(SelfUpdatePanel);
    await flushPromises();
    await button(wrapper, 'Update')!.trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('nothing newer');
  });

  it('waits for the agent to restart, then checks again', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('confirm', () => true);
    let up = true;
    const fetch = stubApi({
      'GET /environments/2/agent/update': () =>
        up ? { body: state() } : { status: 503, body: { error: 'offline' } },
      'POST /environments/2/agent/update': { body: { started: true } },
      'POST /environments/2/agent/update/check': { body: state({ version: 'v1.3.0', status: 'current' }) },
    });
    const wrapper = mount(SelfUpdatePanel, { props: { env: '2' } });
    await flushPromises();
    await button(wrapper, 'Update')!.trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('restarts on the new image');
    up = false;
    await vi.advanceTimersByTimeAsync(2000);
    up = true;
    await vi.advanceTimersByTimeAsync(2000);
    await flushPromises();
    expect(fetch.mock.calls.map((c) => `${c[1]?.method ?? 'GET'} ${c[0]}`)).toContain(
      'POST /api/v1/environments/2/agent/update/check',
    );
    expect(wrapper.text()).toContain('v1.3.0');
    expect(wrapper.text()).toContain('The agent restarted.');
  });

  it.each([
    { name: 'reloads on a new version', version: 'v1.3.0', reloads: true, note: '' },
    { name: 'stays on a rollback', version: 'v1.2.0', reloads: false, note: 'nudge is back on v1.2.0' },
  ])('waits for the server to restart and $name', async ({ version, reloads, note }) => {
    vi.useFakeTimers();
    vi.stubGlobal('confirm', () => true);
    const reload = vi.spyOn(page, 'reload').mockImplementation(() => {});
    let up = true;
    stubApi({
      'GET /system/update': { body: state() },
      'POST /system/update': { body: { started: true } },
      'GET /health': () => (up ? { body: { status: 'ok', version } } : { status: 502, raw: 'Bad Gateway' }),
    });
    const wrapper = mount(SelfUpdatePanel);
    await flushPromises();
    up = false;
    await button(wrapper, 'Update')!.trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('reconnects when it is back');
    await vi.advanceTimersByTimeAsync(2000);
    expect(restarting.value).not.toBeNull();
    up = true;
    await vi.advanceTimersByTimeAsync(4000);
    await flushPromises();
    expect(restarting.value).toBeNull();
    expect(reload).toHaveBeenCalledTimes(reloads ? 1 : 0);
    if (note) expect(wrapper.text()).toContain(note);
    reload.mockRestore();
  });

  it('does nothing when the update is not confirmed', async () => {
    vi.stubGlobal('confirm', () => false);
    const fetch = stubApi({ 'GET /system/update': { body: state() } });
    const wrapper = mount(SelfUpdatePanel);
    await flushPromises();
    await button(wrapper, 'Update')!.trigger('click');
    await flushPromises();
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
