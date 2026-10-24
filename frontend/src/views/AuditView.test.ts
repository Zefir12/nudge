import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { environments } from '../environments';
import { stubApi } from '../test-utils';
import AuditView from './AuditView.vue';

afterEach(() => {
  vi.unstubAllGlobals();
  environments.value = null;
});

const entry = (id: number, extra = {}) => ({
  id,
  time: '2026-10-24T12:00:00Z',
  user: 'admin',
  token: '',
  environment: '1',
  action: 'POST /api/v1/environments/{env}/containers/{id}/start',
  target: 'web',
  status: 204,
  ...extra,
});

const local = { id: 1, name: 'local', kind: 'local', created_at: '2026-10-24T12:00:00Z' };

describe('AuditView', () => {
  it('lists entries with environment names and token users', async () => {
    stubApi({
      'GET /audit': { body: { entries: [entry(2, { token: 'ci', status: 409 }), entry(1)], next: null } },
      'GET /environments': { body: [local] },
    });
    const wrapper = mount(AuditView);
    await flushPromises();
    const text = wrapper.text();
    expect(text).toContain('POST /environments/{env}/containers/{id}/start');
    expect(text).toContain('(token ci)');
    expect(text).toContain('local');
    expect(wrapper.find('td.failed').text()).toBe('409');
    expect(wrapper.find('.more').exists()).toBe(false);
  });

  it('shows an empty log', async () => {
    stubApi({ 'GET /audit': { body: { entries: [], next: null } }, 'GET /environments': { body: [] } });
    const wrapper = mount(AuditView);
    await flushPromises();
    expect(wrapper.text()).toContain('No entries.');
  });

  it('loads the next page', async () => {
    stubApi({
      'GET /audit': { body: { entries: [entry(3), entry(2)], next: 2 } },
      'GET /audit?before=2': { body: { entries: [entry(1, { target: 'old' })], next: null } },
      'GET /environments': { body: [local] },
    });
    const wrapper = mount(AuditView);
    await flushPromises();
    await wrapper.find('.more').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('tbody tr')).toHaveLength(3);
    expect(wrapper.text()).toContain('old');
    expect(wrapper.find('.more').exists()).toBe(false);
  });

  it('filters', async () => {
    const fetch = stubApi({
      'GET /audit': { body: { entries: [entry(1)], next: null } },
      'GET /audit?user=bob&q=volumes&failed=true': { body: { entries: [], next: null } },
      'GET /environments': { body: [local] },
    });
    const wrapper = mount(AuditView);
    await flushPromises();
    await wrapper.find('input[name=user]').setValue(' bob ');
    await wrapper.find('input[name=q]').setValue('volumes');
    await wrapper.find('select[name=result]').setValue('failed');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(fetch).toHaveBeenCalledWith('/api/v1/audit?user=bob&q=volumes&failed=true', expect.anything());
    expect(wrapper.text()).toContain('No entries.');
  });

  it('shows errors', async () => {
    stubApi({
      'GET /audit': { status: 500, body: { error: 'internal error' } },
      'GET /environments': { body: [] },
    });
    const wrapper = mount(AuditView);
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toContain('internal error');
  });
});
