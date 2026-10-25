import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import type { DeployHook, DeployHookWithSecret } from '../api/client';
import { stubApi } from '../test-utils';
import WebhooksCard from './WebhooksCard.vue';

const hook = (over: Partial<DeployHook> = {}): DeployHook => ({
  id: 'K3abc',
  name: 'ci',
  kind: 'container',
  target: 'web',
  tag_filter: '1.*',
  created_by: 'admin',
  created_at: '2026-10-24T12:00:00Z',
  last_triggered_at: '2026-10-25T12:00:00Z',
  last_status: 'updated',
  url: 'https://nudge.example.com/api/v1/hooks/K3abc',
  ...over,
});

const withSecret = (over: Partial<DeployHookWithSecret> = {}): DeployHookWithSecret => ({
  ...hook({ last_triggered_at: null, last_status: '' }),
  secret: 'nudge_hook_s3cret',
  ...over,
});

const listPath = 'GET /environments/1/webhooks?container=web';

async function mountCard(kind: 'container' | 'stack' = 'container', target = 'web') {
  const wrapper = mount(WebhooksCard, { props: { env: '1', kind, target } });
  await flushPromises();
  return wrapper;
}

function button(wrapper: Awaited<ReturnType<typeof mountCard>>, label: string) {
  const b = wrapper.findAll('button').find((b) => b.text() === label);
  if (!b) throw new Error(`no button ${label}`);
  return b;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('WebhooksCard', () => {
  it('lists the webhooks with their last run', async () => {
    stubApi({
      [listPath]: {
        body: [
          hook(),
          hook({ id: 'K3def', name: 'forgejo', tag_filter: '', last_triggered_at: null, last_status: '' }),
        ],
      },
    });
    const wrapper = await mountCard();
    const rows = wrapper.findAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(rows[0]?.text()).toContain('ci');
    expect(rows[0]?.text()).toContain('1.*');
    expect(rows[0]?.find('.badge.good').text()).toBe('updated');
    expect(rows[1]?.text()).toContain('any');
    expect(rows[1]?.text()).toContain('none');
    expect(rows[1]?.text()).toContain('never');
    // No secret is shown outside a create or rotate.
    expect(wrapper.find('.created').exists()).toBe(false);
  });

  it.each([
    { kind: 'container' as const, target: 'web', path: 'GET /environments/1/webhooks?container=web' },
    { kind: 'stack' as const, target: 'my app', path: 'GET /environments/1/webhooks?stack=my+app' },
  ])('asks for the $kind’s webhooks', async ({ kind, target, path }) => {
    const fetch = stubApi({ [path]: { body: [] } });
    const wrapper = await mountCard(kind, target);
    expect(fetch).toHaveBeenCalledWith(`/api/v1${path.slice(4)}`, expect.anything());
    expect(wrapper.text()).toContain('No deploy webhooks yet.');
  });

  it('creates a webhook and shows its secret once', async () => {
    let created = false;
    let sent: unknown;
    stubApi({
      [listPath]: () => ({ body: created ? [hook({ last_status: '', last_triggered_at: null })] : [] }),
      'POST /environments/1/webhooks': (init) => {
        created = true;
        sent = JSON.parse(String(init?.body));
        return { status: 201, body: withSecret() };
      },
    });
    const wrapper = await mountCard();
    await wrapper.find('input[name="name"]').setValue(' ci ');
    await wrapper.find('input[name="tag_filter"]').setValue('1.*');
    await wrapper.find('form').trigger('submit');
    await flushPromises();

    expect(sent).toEqual({ name: 'ci', tag_filter: '1.*', container: 'web' });
    expect(wrapper.find('.secret').text()).toBe('nudge_hook_s3cret');
    expect(wrapper.find('.url').text()).toBe('https://nudge.example.com/api/v1/hooks/K3abc');
    // The step uses the secret from the repository's secrets, never the value itself.
    const step = wrapper.find('pre.snippet').element.textContent ?? '';
    expect(step).toContain(
      'curl -fsS -X POST -H "Authorization: Bearer ${{ secrets.NUDGE_HOOK }}" "https://nudge.example.com/api/v1/hooks/K3abc?wait=1"',
    );
    expect(step).not.toContain('nudge_hook_s3cret');
    expect(wrapper.text()).toContain('application/json');
    expect(wrapper.text()).toContain('Package');
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
    expect((wrapper.find('input[name="name"]').element as HTMLInputElement).value).toBe('');

    await button(wrapper, 'Done').trigger('click');
    expect(wrapper.find('.created').exists()).toBe(false);
  });

  it('shows why a create was refused', async () => {
    stubApi({
      [listPath]: { body: [] },
      'POST /environments/1/webhooks': { status: 409, body: { error: 'a webhook named ci exists' } },
    });
    const wrapper = await mountCard();
    await wrapper.find('input[name="name"]').setValue('ci');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toBe('a webhook named ci exists');
    expect(wrapper.find('.created').exists()).toBe(false);
  });

  it.each([
    { label: 'Copy URL', want: 'https://nudge.example.com/api/v1/hooks/K3abc' },
    { label: 'Copy secret', want: 'nudge_hook_s3cret' },
  ])('$label copies it', async ({ label, want }) => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    stubApi({
      [listPath]: { body: [] },
      'POST /environments/1/webhooks': { status: 201, body: withSecret() },
    });
    const wrapper = await mountCard();
    await wrapper.find('input[name="name"]').setValue('ci');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    await button(wrapper, label).trigger('click');
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith(want);
    expect(wrapper.findAll('button').filter((b) => b.text() === 'Copied')).toHaveLength(1);
  });

  it('rotates the secret after a confirm', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const fetch = stubApi({
      [listPath]: { body: [hook()] },
      'POST /environments/1/webhooks/K3abc/secret': { body: withSecret({ secret: 'nudge_hook_n3w' }) },
    });
    const wrapper = await mountCard();
    await button(wrapper, 'New secret').trigger('click');
    await flushPromises();
    expect(fetch).not.toHaveBeenCalledWith('/api/v1/environments/1/webhooks/K3abc/secret', expect.anything());

    confirm.mockReturnValue(true);
    await button(wrapper, 'New secret').trigger('click');
    await flushPromises();
    expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('stops working'));
    expect(wrapper.find('.secret').text()).toBe('nudge_hook_n3w');
  });

  it('deletes after a confirm', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    stubApi({
      [listPath]: () => ({ body: deleted ? [] : [hook()] }),
      'DELETE /environments/1/webhooks/K3abc': () => {
        deleted = true;
        return { status: 204 };
      },
    });
    const wrapper = await mountCard();
    await button(wrapper, 'Delete').trigger('click');
    await flushPromises();
    expect(deleted).toBe(true);
    expect(wrapper.text()).toContain('No deploy webhooks yet.');
  });

  it('shows the runs on demand', async () => {
    const fetch = stubApi({
      [listPath]: { body: [hook({ kind: 'stack', target: 'app' })] },
      'GET /environments/1/webhooks/K3abc/runs': {
        body: [
          {
            id: 2,
            trigger: 'signature',
            status: 'failed',
            queued_at: '2026-10-25T12:00:00Z',
            started_at: '2026-10-25T12:00:00Z',
            finished_at: '2026-10-25T12:00:05Z',
            image_before: '',
            image_after: '',
            error: 'pull failed',
          },
          {
            id: 1,
            trigger: 'bearer',
            status: 'updated',
            queued_at: '2026-10-24T12:00:00Z',
            started_at: '2026-10-24T12:00:00Z',
            finished_at: '2026-10-24T12:00:05Z',
            image_before: 'app=sha256:aaaaaaaaaaaaaaaa,db=sha256:cccccccccccccccc',
            image_after: 'app=sha256:bbbbbbbbbbbbbbbb,db=sha256:dddddddddddddddd',
            error: '',
          },
        ],
      },
    });
    const wrapper = await mountCard();
    // Not fetched with the list.
    expect(fetch).toHaveBeenCalledTimes(1);

    await button(wrapper, 'Runs').trigger('click');
    await flushPromises();
    const runs = wrapper.findAll('tr.runs tbody tr');
    expect(runs).toHaveLength(2);
    expect(runs[0]?.find('.badge.bad').text()).toBe('failed');
    expect(runs[0]?.text()).toContain('pull failed');
    expect(runs[1]?.text()).toContain(
      'app=aaaaaaaaaaaa, db=cccccccccccc → app=bbbbbbbbbbbb, db=dddddddddddd',
    );

    await button(wrapper, 'Runs').trigger('click');
    expect(wrapper.find('tr.runs').exists()).toBe(false);
  });

  it('stays collapsed when closed while the runs load', async () => {
    let answer: (r: Response) => void = () => {};
    stubApi({ [listPath]: { body: [hook()] } });
    const wrapper = await mountCard();
    vi.stubGlobal('fetch', () => new Promise<Response>((resolve) => (answer = resolve)));
    await button(wrapper, 'Runs').trigger('click');
    await button(wrapper, 'Runs').trigger('click');
    answer(new Response('[]', { status: 200 }));
    await flushPromises();
    expect(wrapper.find('tr.runs').exists()).toBe(false);
  });

  it('shows why the runs failed to load', async () => {
    stubApi({
      [listPath]: { body: [hook()] },
      'GET /environments/1/webhooks/K3abc/runs': { status: 404, body: { error: 'webhook not found' } },
    });
    const wrapper = await mountCard();
    await button(wrapper, 'Runs').trigger('click');
    await flushPromises();
    expect(wrapper.find('tr.runs [role="alert"]').text()).toBe('webhook not found');
  });
});
