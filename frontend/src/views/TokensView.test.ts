import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import TokensView from './TokensView.vue';

afterEach(() => {
  vi.unstubAllGlobals();
});

const ci = {
  id: 1,
  name: 'ci',
  prefix: 'nudge_AB12',
  created_at: '2026-10-24T12:00:00Z',
  last_used_at: null,
};

describe('TokensView', () => {
  it('lists tokens without their secret', async () => {
    stubApi({ 'GET /tokens': { body: [ci] } });
    const wrapper = mount(TokensView);
    await flushPromises();
    expect(wrapper.text()).toContain('ci');
    expect(wrapper.text()).toContain('nudge_AB12…');
    expect(wrapper.text()).toContain('never');
  });

  it('shows an empty list', async () => {
    stubApi({ 'GET /tokens': { body: [] } });
    const wrapper = mount(TokensView);
    await flushPromises();
    expect(wrapper.text()).toContain('No tokens yet.');
  });

  it('shows a new token once', async () => {
    let tokens: unknown[] = [];
    stubApi({
      'GET /tokens': () => ({ body: tokens }),
      'POST /tokens': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({ name: 'ci' });
        tokens = [ci];
        return { status: 201, body: { ...ci, token: 'nudge_AB12secretsecret' } };
      },
    });
    const wrapper = mount(TokensView);
    await flushPromises();
    await wrapper.find('input[name=name]').setValue(' ci ');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('.secret').text()).toBe('nudge_AB12secretsecret');
    expect(wrapper.find('tbody').text()).toContain('ci');

    const done = wrapper.findAll('button').find((b) => b.text() === 'Done');
    await done?.trigger('click');
    expect(wrapper.text()).not.toContain('nudge_AB12secretsecret');
  });

  it('shows a create error', async () => {
    stubApi({
      'GET /tokens': { body: [ci] },
      'POST /tokens': { status: 409, body: { error: 'a token with that name already exists' } },
    });
    const wrapper = mount(TokensView);
    await flushPromises();
    await wrapper.find('input[name=name]').setValue('ci');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('a token with that name already exists');
  });

  it('revokes after confirmation', async () => {
    let tokens: unknown[] = [ci];
    const fetch = stubApi({
      'GET /tokens': () => ({ body: tokens }),
      'DELETE /tokens/1': () => {
        tokens = [];
        return { status: 204 };
      },
    });
    const confirm = vi.fn(() => true);
    vi.stubGlobal('confirm', confirm);
    const wrapper = mount(TokensView);
    await flushPromises();
    await wrapper.find('button.danger').trigger('click');
    await flushPromises();
    expect(confirm).toHaveBeenCalled();
    expect(fetch).toHaveBeenCalledWith('/api/v1/tokens/1', expect.objectContaining({ method: 'DELETE' }));
    expect(wrapper.text()).toContain('No tokens yet.');
  });

  it('keeps the token when revoking is cancelled', async () => {
    const fetch = stubApi({ 'GET /tokens': { body: [ci] } });
    vi.stubGlobal(
      'confirm',
      vi.fn(() => false),
    );
    const wrapper = mount(TokensView);
    await flushPromises();
    await wrapper.find('button.danger').trigger('click');
    await flushPromises();
    expect(fetch).not.toHaveBeenCalledWith('/api/v1/tokens/1', expect.anything());
  });
});
