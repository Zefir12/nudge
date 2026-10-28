import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { currentUser } from '../session';
import { stubApi, user } from '../test-utils';
import AccountView from './AccountView.vue';

beforeEach(() => {
  currentUser.value = user;
});

afterEach(() => {
  currentUser.value = null;
  vi.unstubAllGlobals();
});

async function fill(wrapper: ReturnType<typeof mount>, current: string, next: string, repeat = next) {
  await wrapper.find('input[name=current]').setValue(current);
  await wrapper.find('input[name=new]').setValue(next);
  await wrapper.find('input[name=repeat]').setValue(repeat);
}

describe('AccountView', () => {
  it('changes the password', async () => {
    const fetch = stubApi({ 'POST /auth/password': { status: 204 } });
    const wrapper = mount(AccountView);
    expect(wrapper.text()).toContain('Signed in as bob (user)');
    await fill(wrapper, 'longenough', 'evenlonger');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(JSON.parse(String(fetch.mock.calls[0]?.[1]?.body))).toEqual({
      current: 'longenough',
      new: 'evenlonger',
    });
    expect(wrapper.text()).toContain('Password changed.');
    expect((wrapper.find('input[name=current]').element as HTMLInputElement).value).toBe('');
  });

  it('needs the new password twice and long enough', async () => {
    stubApi({});
    const wrapper = mount(AccountView);
    const button = () => wrapper.find('button[type=submit]').element as HTMLButtonElement;
    await fill(wrapper, 'longenough', 'short');
    expect(button().disabled).toBe(true);
    await fill(wrapper, 'longenough', 'evenlonger', 'evenlonge');
    expect(button().disabled).toBe(true);
    expect(wrapper.text()).toContain('The new passwords differ.');
    await fill(wrapper, 'longenough', 'evenlonger');
    expect(button().disabled).toBe(false);
  });

  it('shows a wrong current password', async () => {
    stubApi({ 'POST /auth/password': { status: 403, body: { error: 'the current password is wrong' } } });
    const wrapper = mount(AccountView);
    await fill(wrapper, 'not-it-at-all', 'evenlonger');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('the current password is wrong');
  });

  it('has no password form for SSO users', () => {
    currentUser.value = { ...user, sso: true };
    stubApi({});
    const wrapper = mount(AccountView);
    expect(wrapper.find('form').exists()).toBe(false);
    expect(wrapper.text()).toContain('single sign-on');
  });
});
