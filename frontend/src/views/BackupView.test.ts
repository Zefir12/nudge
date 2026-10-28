import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import BackupView from './BackupView.vue';

afterEach(() => {
  vi.unstubAllGlobals();
});

async function fill(wrapper: ReturnType<typeof mount>, pass: string, again = pass) {
  await wrapper.find('input[name=passphrase]').setValue(pass);
  await wrapper.find('input[name=confirm]').setValue(again);
}

describe('BackupView', () => {
  it('downloads a backup encrypted with the passphrase', async () => {
    const fetch = stubApi({
      'POST /backup': {
        raw: 'NUDGEBAK…',
        headers: { 'Content-Disposition': 'attachment; filename="nudge-20261028-120000.nudgebak"' },
      },
    });
    const create = vi.fn(() => 'blob:backup');
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    const wrapper = mount(BackupView);
    await fill(wrapper, 'correct horse battery');
    await wrapper.find('form').trigger('submit');
    await flushPromises();

    expect(JSON.parse(String(fetch.mock.calls[0]?.[1]?.body))).toEqual({
      passphrase: 'correct horse battery',
    });
    expect(create).toHaveBeenCalledOnce();
    expect(click).toHaveBeenCalledOnce();
    expect(wrapper.text()).toContain('Saved nudge-20261028-120000.nudgebak.');
    expect((wrapper.find('input[name=passphrase]').element as HTMLInputElement).value).toBe('');
    click.mockRestore();
  });

  it('needs a long passphrase typed twice', async () => {
    stubApi({});
    const wrapper = mount(BackupView);
    const button = () => wrapper.find('button[type=submit]').element as HTMLButtonElement;
    await fill(wrapper, 'short', 'short');
    expect(button().disabled).toBe(true);
    await fill(wrapper, 'correct horse battery', 'correct horse');
    expect(button().disabled).toBe(true);
    expect(wrapper.text()).toContain('The passphrases differ.');
    await fill(wrapper, 'correct horse battery');
    expect(button().disabled).toBe(false);
  });

  it('shows the server error', async () => {
    stubApi({ 'POST /backup': { status: 409, body: { error: 'a backup is already running' } } });
    const wrapper = mount(BackupView);
    await fill(wrapper, 'correct horse battery');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe('a backup is already running');
  });
});
