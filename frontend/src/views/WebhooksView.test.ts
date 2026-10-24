import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import WebhooksView from './WebhooksView.vue';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const chat = {
  id: 1,
  name: 'Chat',
  endpoint: 'https://hooks.example.com',
  header_name: 'X-Token',
  enabled: true,
  last_sent_at: null,
  last_error: '',
  created_at: '2026-10-24T12:00:00Z',
  updated_at: '2026-10-24T12:00:00Z',
};

const button = (wrapper: ReturnType<typeof mount>, text: string) =>
  wrapper.findAll('button').find((b) => b.text() === text);

describe('WebhooksView', () => {
  it('lists webhooks', async () => {
    stubApi({
      'GET /webhooks': { body: [chat, { ...chat, id: 2, name: 'Off', header_name: '', enabled: false }] },
    });
    const wrapper = mount(WebhooksView);
    await flushPromises();
    const text = wrapper.find('tbody').text();
    expect(text).toContain('Chat');
    expect(text).toContain('https://hooks.example.com');
    expect(text).toContain('with X-Token');
    expect(text).toContain('Disabled');
    expect(text).toContain('never');
  });

  it('adds a webhook with a secret header', async () => {
    let list: unknown[] = [];
    stubApi({
      'GET /webhooks': () => ({ body: list }),
      'POST /webhooks': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({
          name: 'Chat',
          url: 'https://hooks.example.com/abc',
          header_name: 'X-Token',
          header_value: 'hunter2',
          enabled: true,
        });
        list = [chat];
        return { status: 201, body: chat };
      },
    });
    const wrapper = mount(WebhooksView);
    await flushPromises();
    expect(wrapper.text()).toContain('No webhooks yet.');
    await wrapper.find('input[name=name]').setValue(' Chat ');
    await wrapper.find('input[name=url]').setValue('https://hooks.example.com/abc');
    await wrapper.find('input[name=header_name]').setValue('X-Token');
    await wrapper.find('input[name=header_value]').setValue('hunter2');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('tbody').text()).toContain('Chat');
    expect((wrapper.find('input[name=header_value]').element as HTMLInputElement).value).toBe('');
  });

  it('edits a webhook keeping its URL and header value', async () => {
    stubApi({
      'GET /webhooks': { body: [chat] },
      'PUT /webhooks/1': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({
          name: 'Chat',
          header_name: 'X-Token',
          enabled: false,
        });
        return { body: { ...chat, enabled: false } };
      },
    });
    const wrapper = mount(WebhooksView);
    await flushPromises();
    await button(wrapper, 'Edit')?.trigger('click');
    expect(wrapper.text()).toContain('Edit Chat');
    expect(wrapper.find('input[name=header_value]').attributes('placeholder')).toBe('unchanged');
    await wrapper.find('input[name=enabled]').setValue(false);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Add a webhook');
  });

  it('shows the outcome of a test', async () => {
    let ok = true;
    stubApi({
      'GET /webhooks': { body: [chat] },
      'POST /webhooks/1/test': () => ({
        body: ok ? { ok } : { ok, error: 'the webhook answered with status 403' },
      }),
    });
    const wrapper = mount(WebhooksView);
    await flushPromises();
    await button(wrapper, 'Test')?.trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=status]').text()).toBe('Test notification delivered.');
    ok = false;
    await button(wrapper, 'Test')?.trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=status]').text()).toBe('the webhook answered with status 403');
  });

  it('deletes after confirming', async () => {
    let list: unknown[] = [chat];
    stubApi({
      'GET /webhooks': () => ({ body: list }),
      'DELETE /webhooks/1': () => {
        list = [];
        return { status: 204 };
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = mount(WebhooksView);
    await flushPromises();
    await button(wrapper, 'Delete')?.trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('No webhooks yet.');
  });

  it('shows API errors', async () => {
    stubApi({
      'GET /webhooks': { body: [] },
      'POST /webhooks': {
        status: 400,
        body: { error: 'url must be an http or https URL without credentials' },
      },
    });
    const wrapper = mount(WebhooksView);
    await flushPromises();
    await wrapper.find('input[name=name]').setValue('x');
    await wrapper.find('input[name=url]').setValue('ftp://x');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toContain('url must be');
  });
});
