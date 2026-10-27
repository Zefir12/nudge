import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import UsersView from './UsersView.vue';

const mountView = () => mount(UsersView, { global: { plugins: [createAppRouter(createMemoryHistory())] } });

const account = (over: object) => ({
  id: 1,
  username: 'admin',
  role: 'admin',
  disabled: false,
  sso: false,
  display_name: '',
  email: '',
  created_at: '2026-10-24T12:00:00Z',
  ...over,
});
const adminAccount = account({});
const bob = account({ id: 2, username: 'bob', role: 'user' });
const carol = account({ id: 3, username: 'carol', role: 'user', sso: true, email: 'carol@example.net' });

const button = (row: ReturnType<ReturnType<typeof mount>['find']>, text: string) =>
  row.findAll('button').find((b) => b.text() === text);
const rowOf = (wrapper: ReturnType<typeof mount>, name: string) =>
  wrapper.findAll('tbody tr').find((r) => r.text().includes(name))!;

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('UsersView', () => {
  it('lists users; an SSO user has no password and a fixed role', async () => {
    stubApi({ 'GET /users': { body: [adminAccount, bob, carol] } });
    const wrapper = mountView();
    await flushPromises();
    expect(rowOf(wrapper, 'admin').text()).toContain('(you)');
    const sso = rowOf(wrapper, 'carol');
    expect(sso.text()).toContain('SSO');
    expect(sso.text()).toContain('carol@example.net');
    expect(sso.find('select').attributes('disabled')).toBeDefined();
    expect(button(sso, 'Reset password')).toBeUndefined();
    expect(button(rowOf(wrapper, 'bob'), 'Reset password')).toBeDefined();
  });

  it('adds a user', async () => {
    let list = [adminAccount];
    stubApi({
      'GET /users': () => ({ body: list }),
      'POST /users': (init) => {
        expect(JSON.parse(String(init?.body))).toEqual({
          username: 'bob',
          password: 'longenough',
          role: 'admin',
        });
        list = [adminAccount, bob];
        return { status: 201, body: bob };
      },
    });
    const wrapper = mountView();
    await flushPromises();
    await wrapper.find('input[name="username"]').setValue(' bob ');
    await wrapper.find('input[name="password"]').setValue('longenough');
    await wrapper.find('select[name="role"]').setValue('admin');
    await wrapper.find('form.user').trigger('submit');
    await flushPromises();
    expect(wrapper.find('tbody').text()).toContain('bob');
  });

  it('changes a role and shows a refusal', async () => {
    stubApi({
      'GET /users': { body: [adminAccount, bob] },
      'PATCH /users/1': {
        status: 409,
        body: { error: "the last enabled admin can't be demoted, disabled or deleted" },
      },
    });
    const wrapper = mountView();
    await flushPromises();
    await rowOf(wrapper, 'admin').find('select').setValue('user');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain('last enabled admin');
  });

  it('disables, resets a password and deletes', async () => {
    const calls: string[] = [];
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    stubApi({
      'GET /users': { body: [adminAccount, bob] },
      'PATCH /users/2': (init) => {
        calls.push(`PATCH ${String(init?.body)}`);
        return { body: bob };
      },
      'DELETE /users/2': () => {
        calls.push('DELETE');
        return { status: 204 };
      },
    });
    const wrapper = mountView();
    await flushPromises();
    await button(rowOf(wrapper, 'bob'), 'Disable')!.trigger('click');
    await flushPromises();
    await button(rowOf(wrapper, 'bob'), 'Reset password')!.trigger('click');
    await wrapper.find('input[name="new_password"]').setValue('evenlonger');
    await wrapper.find('form.reset').trigger('submit');
    await flushPromises();
    expect(wrapper.find('form.reset').exists()).toBe(false);
    await button(rowOf(wrapper, 'bob'), 'Delete')!.trigger('click');
    await flushPromises();
    expect(calls).toEqual(['PATCH {"disabled":true}', 'PATCH {"password":"evenlonger"}', 'DELETE']);
  });

  it("sets the environments a user may use; admins don't need any", async () => {
    let saved: unknown;
    stubApi({
      'GET /users': { body: [adminAccount, bob] },
      'GET /environments': {
        body: [
          { id: 1, name: 'local' },
          { id: 2, name: 'edge' },
        ],
      },
      'GET /users/2/environments': { body: { environments: [1] } },
      'PUT /users/2/environments': (init) => {
        saved = JSON.parse(String(init?.body));
        return { body: saved };
      },
    });
    const wrapper = mountView();
    await flushPromises();
    expect(button(rowOf(wrapper, 'admin'), 'Environments')).toBeUndefined();
    await button(rowOf(wrapper, 'bob'), 'Environments')!.trigger('click');
    await flushPromises();
    const local = wrapper.find<HTMLInputElement>('input[name="env-1"]');
    expect(local.element.checked).toBe(true);
    expect(wrapper.find<HTMLInputElement>('input[name="env-2"]').element.checked).toBe(false);
    await local.setValue(false);
    await wrapper.find('input[name="env-2"]').setValue(true);
    await wrapper.find('form.access').trigger('submit');
    await flushPromises();
    expect(saved).toEqual({ environments: [2] });
    expect(wrapper.find('form.access').exists()).toBe(false);
  });

  it("edits a user's allowance per environment", async () => {
    const usage = { cpus_milli: 500, memory_bytes: 256 * 1024 ** 2, disk_bytes: 0, containers: 1 };
    let limits: object | null = null;
    let sent: unknown;
    stubApi({
      'GET /users': { body: [adminAccount, bob] },
      'GET /environments': {
        body: [{ id: 4, name: 'shared', kind: 'local', online: true, created_at: '', last_seen_at: null }],
      },
      'GET /environments/4/allowance?user=2': () => ({ body: { user: 2, limits, used: usage, left: null } }),
      'PUT /environments/4/allowance?user=2': (init) => {
        sent = JSON.parse(String(init?.body));
        limits = { ...(sent as object), updated_at: '2026-10-27T12:00:00Z' };
        return { body: limits };
      },
      'DELETE /environments/4/allowance?user=2': () => {
        limits = null;
        return { status: 204 };
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = mountView();
    await flushPromises();
    expect(button(rowOf(wrapper, 'admin'), 'Allowance')).toBeUndefined();
    await button(rowOf(wrapper, 'bob'), 'Allowance')!.trigger('click');
    await flushPromises();
    const form = wrapper.find('form.allowance');
    expect(form.text()).toContain('Allowance of bob');
    await form.find('input[name="cpus"]').setValue('1.5');
    await form.find('input[name="memory"]').setValue('8192');
    await form.find('input[name="disk"]').setValue('20');
    await form.find('input[name="containers"]').setValue('5');
    await form.find('input[name="port_min"]').setValue('8000');
    await form.find('input[name="port_max"]').setValue('8009');
    await form.find('textarea[name="images"]').setValue('nginx\n ghcr.io/me/* ');
    await form.trigger('submit');
    await flushPromises();
    expect(sent).toEqual({
      cpus_milli: 1500,
      memory_bytes: 8 * 1024 ** 3,
      disk_bytes: 20 * 1024 ** 3,
      max_containers: 5,
      port_min: 8000,
      port_max: 8009,
      images: ['nginx', 'ghcr.io/me/*'],
    });
    expect(wrapper.find('form.allowance').text()).toContain('0.5 / 1.5');

    await button(wrapper.find('form.allowance'), 'Remove allowance')!.trigger('click');
    await flushPromises();
    expect(button(wrapper.find('form.allowance'), 'Remove allowance')).toBeUndefined();
    await button(wrapper.find('form.allowance'), 'Close')!.trigger('click');
    expect(wrapper.find('form.allowance').exists()).toBe(false);
  });
});
