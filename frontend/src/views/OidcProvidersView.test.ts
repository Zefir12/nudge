import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import OidcProvidersView from './OidcProvidersView.vue';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const authestra = {
  id: 'authestra',
  name: 'Authestra',
  issuer: 'https://auth.example.net',
  client_id: 'nudge',
  admin_role: 'admin',
  user_role: 'user',
  require_mfa: false,
  session_ttl_minutes: 720,
  enabled: true,
  read_only: false,
  secret_unreadable: false,
  callback_url: 'https://nudge.example.net/api/v1/auth/oidc/authestra/callback',
};
const fromEnv = {
  ...authestra,
  id: 'env',
  name: 'Corp',
  read_only: true,
  callback_url: 'https://nudge.example.net/api/v1/auth/oidc/callback',
};
const list = {
  callback_prefix: 'https://nudge.example.net/api/v1/auth/oidc/',
  providers: [fromEnv, authestra],
};

const button = (wrapper: ReturnType<typeof mount>, text: string) =>
  wrapper.findAll('button').filter((b) => b.text() === text);

const bodyOf = (fetch: ReturnType<typeof stubApi>, method: string, url: string) => {
  const call = fetch.mock.calls.find(([u, init]) => u === url && init?.method === method);
  return JSON.parse(String(call?.[1]?.body));
};

describe('OidcProvidersView', () => {
  it('lists providers and offers no changes to the environment one', async () => {
    stubApi({ 'GET /oidc-providers': { body: list } });
    const wrapper = mount(OidcProvidersView);
    await flushPromises();
    const rows = wrapper.findAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('from NUDGE_OIDC_*');
    expect(rows[0]!.findAll('button').map((b) => b.text())).not.toContain('Edit');
    expect(rows[1]!.findAll('.row-actions button').map((b) => b.text())).toEqual([
      'Edit',
      'Disable',
      'Delete',
    ]);
  });

  it('previews the callback URL from the name and sends the new provider', async () => {
    const fetch = stubApi({
      'GET /oidc-providers': { body: { ...list, providers: [] } },
      'POST /oidc-providers': { status: 201, body: authestra },
    });
    const wrapper = mount(OidcProvidersView);
    await flushPromises();
    await wrapper.find('input[name=name]').setValue('My Auth!');
    expect(wrapper.find('.callback').text()).toContain(
      'https://nudge.example.net/api/v1/auth/oidc/my-auth/callback',
    );
    await wrapper.find('input[name=issuer]').setValue('https://auth.example.net');
    await wrapper.find('input[name=client_id]').setValue('nudge');
    await wrapper.find('input[name=client_secret]').setValue('s3cret');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(bodyOf(fetch, 'POST', '/api/v1/oidc-providers')).toEqual({
      id: 'my-auth',
      name: 'My Auth!',
      issuer: 'https://auth.example.net',
      client_id: 'nudge',
      client_secret: 's3cret',
      admin_role: 'admin',
      user_role: 'user',
      require_mfa: false,
      session_ttl_minutes: 720,
    });
  });

  it('keeps the stored secret on edit unless the issuer changes', async () => {
    const fetch = stubApi({
      'GET /oidc-providers': { body: list },
      'PUT /oidc-providers/authestra': { body: authestra },
    });
    const wrapper = mount(OidcProvidersView);
    await flushPromises();
    await button(wrapper, 'Edit')[0]!.trigger('click');
    expect((wrapper.find('input[name=client_secret]').element as HTMLInputElement).value).toBe('');
    expect(wrapper.find('input[name=client_secret]').attributes('placeholder')).toBe('unchanged');

    await wrapper.find('input[name=issuer]').setValue('https://other.example.net');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toContain('client secret again');

    await wrapper.find('input[name=issuer]').setValue(authestra.issuer);
    await wrapper.find('input[name=name]').setValue('Renamed');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    const body = bodyOf(fetch, 'PUT', '/api/v1/oidc-providers/authestra');
    expect(body.name).toBe('Renamed');
    expect(body).not.toHaveProperty('client_secret');
  });

  it('asks before disabling and shows a refusal', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const fetch = stubApi({
      'GET /oidc-providers': { body: list },
      'PUT /oidc-providers/authestra': { status: 409, body: { error: 'no enabled admin could sign in' } },
    });
    const wrapper = mount(OidcProvidersView);
    await flushPromises();
    await button(wrapper, 'Disable')[0]!.trigger('click');
    expect(confirm.mock.calls[0]![0]).toContain('API tokens');
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);

    confirm.mockReturnValue(true);
    await button(wrapper, 'Disable')[0]!.trigger('click');
    await flushPromises();
    expect(bodyOf(fetch, 'PUT', '/api/v1/oidc-providers/authestra').enabled).toBe(false);
    expect(wrapper.find('[role=alert]').text()).toContain('no enabled admin');
  });

  it('deletes after asking', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const fetch = stubApi({
      'GET /oidc-providers': { body: list },
      'DELETE /oidc-providers/authestra': { status: 204 },
    });
    const wrapper = mount(OidcProvidersView);
    await flushPromises();
    await button(wrapper, 'Delete')[0]!.trigger('click');
    await flushPromises();
    expect(
      fetch.mock.calls.some(
        ([u, init]) => u === '/api/v1/oidc-providers/authestra' && init?.method === 'DELETE',
      ),
    ).toBe(true);
  });

  it('warns without an https public URL', async () => {
    stubApi({ 'GET /oidc-providers': { body: { callback_prefix: '', providers: [] } } });
    const wrapper = mount(OidcProvidersView);
    await flushPromises();
    expect(wrapper.find('.warn').text()).toContain('https:// public URL');
  });
});
