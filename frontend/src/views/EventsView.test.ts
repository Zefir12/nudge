import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { DockerEvent } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubSockets, type FakeWebSocket } from '../test-utils';
import EventsView from './EventsView.vue';

let sockets: FakeWebSocket[];

beforeEach(() => {
  currentUser.value = admin;
  sockets = stubSockets();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/env/1/events');
  const wrapper = mount(EventsView, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

const events: DockerEvent[] = [
  { time: '2026-10-24T12:00:00Z', type: 'container', action: 'start', id: 'abc123def4567890', name: 'web' },
  { time: '2026-10-24T12:00:01Z', type: 'volume', action: 'create', id: 'data' },
  { time: '2026-10-24T12:00:02Z', type: 'container', action: 'destroy', id: 'fff000', name: 'old' },
];

const rows = (w: Awaited<ReturnType<typeof mountView>>) =>
  w.findAll('tbody tr').map((tr) =>
    tr
      .findAll('td')
      .map((td) => td.text())
      .slice(1),
  );

describe('EventsView', () => {
  it('shows every event, newest first, with links to live objects', async () => {
    const wrapper = await mountView();
    expect(sockets[0]!.url).toMatch(/\/api\/v1\/environments\/1\/events$/);
    expect(wrapper.text()).toContain('Waiting for events');
    for (const e of events) sockets[0]!.message(e);
    await flushPromises();
    expect(rows(wrapper)).toEqual([
      ['container', 'destroy', 'old'],
      ['volume', 'create', 'data'],
      ['container', 'start', 'web'],
    ]);
    const links = wrapper.findAll('tbody a').map((a) => a.attributes('href'));
    expect(links).toEqual(['/env/1/volumes/data', '/env/1/containers/abc123def4567890']);
  });

  it.each([
    { name: 'by type', type: 'volume', query: '', want: ['data'] },
    { name: 'by action', type: '', query: 'START', want: ['web'] },
    { name: 'no match', type: 'image', query: '', want: [] },
  ])('filters $name', async ({ type, query, want }) => {
    const wrapper = await mountView();
    for (const e of events) sockets[0]!.message(e);
    await wrapper.find('select').setValue(type);
    await wrapper.find('input[type=search]').setValue(query);
    expect(rows(wrapper).map((r) => r[2])).toEqual(want);
    if (!want.length) expect(wrapper.text()).toContain('No event matches.');
  });

  it('pauses, clears and shows a dropped stream', async () => {
    const wrapper = await mountView();
    sockets[0]!.message(events[0]);
    const [pause, clear] = wrapper.findAll('button');
    await pause!.trigger('click');
    sockets[0]!.message(events[1]);
    await flushPromises();
    expect(rows(wrapper)).toHaveLength(1);
    expect(wrapper.text()).toContain('Paused');

    await clear!.trigger('click');
    expect(wrapper.text()).toContain('Waiting for events');

    sockets[0]!.message({ error: 'the event stream fell behind; reconnect to resume' });
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toContain('fell behind');
    wrapper.unmount();
  });
});
