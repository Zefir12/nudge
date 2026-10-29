import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import ContainerActions from './ContainerActions.vue';

const mountWith = (props: { state: string; volumesOption?: boolean }) =>
  mount(ContainerActions, { props: { env: '1', id: 'abc123', name: 'web', ...props } });

const buttons = (wrapper: ReturnType<typeof mountWith>) => wrapper.findAll('button').map((b) => b.text());

const button = (wrapper: ReturnType<typeof mountWith>, text: string) => {
  const b = wrapper.findAll('button').find((b) => b.text() === text);
  if (!b) throw new Error(`no ${text} button`);
  return b;
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ContainerActions', () => {
  it.each([
    { state: 'running', want: ['Stop', 'Restart', 'Kill', 'Remove'] },
    { state: 'paused', want: ['Stop', 'Restart', 'Kill', 'Remove'] },
    { state: 'exited', want: ['Start', 'Remove'] },
    { state: 'created', want: ['Start', 'Remove'] },
  ])('offers the actions of a $state container', ({ state, want }) => {
    expect(buttons(mountWith({ state }))).toEqual(want);
  });

  it.each([
    { state: 'running', text: 'Stop', path: '/environments/1/containers/abc123/stop' },
    { state: 'running', text: 'Restart', path: '/environments/1/containers/abc123/restart' },
    { state: 'exited', text: 'Start', path: '/environments/1/containers/abc123/start' },
  ])('$text runs without asking and emits changed', async ({ state, text, path }) => {
    const fetch = stubApi({ [`POST ${path}`]: { status: 204 } });
    const confirm = vi.spyOn(window, 'confirm');
    const wrapper = mountWith({ state });
    await button(wrapper, text).trigger('click');
    await flushPromises();
    expect(confirm).not.toHaveBeenCalled();
    expect(fetch).toHaveBeenCalledWith(`/api/v1${path}`, expect.objectContaining({ method: 'POST' }));
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  // In a table row the actions are icons, named by their title for the pointer and
  // their aria-label for screen readers.
  it.each([
    { state: 'running', want: ['Stop', 'Restart', 'Kill', 'Remove'] },
    { state: 'exited', want: ['Start', 'Remove'] },
  ])('shows the actions of a $state container as icons', ({ state, want }) => {
    const wrapper = mount(ContainerActions, {
      props: { env: '1', id: 'abc123', name: 'web', state, icons: true },
    });
    const buttons = wrapper.findAll('button');
    expect(buttons.map((b) => b.attributes('title'))).toEqual(want);
    expect(buttons.map((b) => b.attributes('aria-label'))).toEqual(want);
    expect(buttons.every((b) => b.classes('icon') && b.text() === '')).toBe(true);
  });

  it('kills only after confirming', async () => {
    const fetch = stubApi({ 'POST /environments/1/containers/abc123/kill': { status: 204 } });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const wrapper = mountWith({ state: 'running' });
    await button(wrapper, 'Kill').trigger('click');
    await flushPromises();
    expect(fetch).not.toHaveBeenCalled();
    await button(wrapper, 'Kill').trigger('click');
    await flushPromises();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  it.each([
    {
      name: 'a stopped container',
      state: 'exited',
      volumes: false,
      path: '/environments/1/containers/abc123',
    },
    {
      name: 'a running one with force',
      state: 'running',
      volumes: false,
      path: '/environments/1/containers/abc123?force=true',
    },
    {
      name: 'with its volumes',
      state: 'exited',
      volumes: true,
      path: '/environments/1/containers/abc123?volumes=true',
    },
  ])('removes $name after confirming', async ({ state, volumes, path }) => {
    const fetch = stubApi({ [`DELETE ${path}`]: { status: 204 } });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = mountWith({ state, volumesOption: true });
    if (volumes) await wrapper.find('input[type=checkbox]').setValue(true);
    await button(wrapper, 'Remove').trigger('click');
    await flushPromises();
    expect(confirm.mock.calls[0]![0]).toContain('Remove "web"?');
    if (state === 'running') expect(confirm.mock.calls[0]![0]).toContain('killed first');
    expect(fetch).toHaveBeenCalledWith(`/api/v1${path}`, expect.objectContaining({ method: 'DELETE' }));
    expect(wrapper.emitted('removed')).toHaveLength(1);
  });

  it('does not remove when the confirmation is cancelled', async () => {
    const fetch = stubApi({});
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    const wrapper = mountWith({ state: 'exited' });
    await button(wrapper, 'Remove').trigger('click');
    await flushPromises();
    expect(fetch).not.toHaveBeenCalled();
    expect(wrapper.emitted('removed')).toBeUndefined();
  });

  it('hides the volumes option unless asked', () => {
    expect(mountWith({ state: 'exited' }).find('input[type=checkbox]').exists()).toBe(false);
  });

  it('shows a failed action', async () => {
    stubApi({
      'DELETE /environments/1/containers/abc123': {
        status: 409,
        body: { error: 'the container is running: stop it first, or remove it with force=true' },
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = mountWith({ state: 'exited' });
    await button(wrapper, 'Remove').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toContain('the container is running');
    expect(wrapper.emitted('removed')).toBeUndefined();
    expect(button(wrapper, 'Remove').attributes('disabled')).toBeUndefined();
  });
});
