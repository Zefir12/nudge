import { flushPromises, mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vite-plus/test';

import EnvFormEditor from './EnvFormEditor.vue';

const example = { path: 'app.env', commit: '9fceb02d0ae598e95dc970b74767f19372d61af8', content: '' };

async function mountForm(env: string, content?: string | Error) {
  const loadExample =
    content === undefined
      ? undefined
      : vi.fn(() =>
          content instanceof Error ? Promise.reject(content) : Promise.resolve({ ...example, content }),
        );
  const wrapper = mount(EnvFormEditor, { props: { env, loadExample } });
  await flushPromises();
  return wrapper;
}

const applied = (w: Awaited<ReturnType<typeof mountForm>>) => w.emitted('apply')?.[0]?.[0];

describe('EnvFormEditor', () => {
  it('edits a .env without an example in place', async () => {
    const w = await mountForm('# the port\nPORT=80\n\nHOST=x # note\n');
    const vars = w.findAll('.var');
    expect(vars.map((v) => v.find('.name').text())).toEqual(['PORT', 'HOST']);
    expect(vars[0]!.find('.comment').text()).toBe('the port');
    expect(w.text()).not.toContain('Other variables');
    await w.find('#env-0-1').setValue('y');
    await w.find('form').trigger('submit');
    expect(applied(w)).toBe('# the port\nPORT=80\n\nHOST=y # note\n');
  });

  it('follows the example and keeps the .env-only variables at the bottom', async () => {
    const w = await mountForm('EXTRA=1\nPORT=81\n', '# the port\nPORT=8080\nDEBUG=false\n');
    expect(w.text()).toContain('app.env');
    expect(w.text()).toContain('9fceb02d0ae5');
    expect(w.findAll('.name').map((n) => n.text())).toEqual(['PORT', 'DEBUG', 'EXTRA']);
    expect(w.find('h3').text()).toBe('Other variables');
    expect((w.find('#env-0-0').element as HTMLInputElement).value).toBe('81');
    expect((w.find('#env-0-1').element as HTMLInputElement).value).toBe('false');
    await w.find('form').trigger('submit');
    expect(applied(w)).toBe('# the port\nPORT=81\nDEBUG=false\n\nEXTRA=1\n');
  });

  it('masks secret-looking values with a show/hide toggle', async () => {
    const w = await mountForm('DB_PASSWORD=s3cret\nPORT=80\n');
    const secret = w.find('#env-0-0');
    expect(secret.attributes('type')).toBe('password');
    expect(w.find('#env-0-1').attributes('type')).toBe('text');
    expect(w.findAll('.toggle')).toHaveLength(1);
    await w.find('.toggle').trigger('click');
    expect(secret.attributes('type')).toBe('text');
    expect(w.find('.toggle').text()).toBe('Hide');
    await w.find('.toggle').trigger('click');
    expect(secret.attributes('type')).toBe('password');
  });

  it('offers the .env alone when the example cannot be read', async () => {
    const w = await mountForm('A=1\n', new Error('app.env is not in the repository'));
    expect(w.find('[role="alert"]').text()).toContain('app.env is not in the repository');
    await w
      .findAll('button')
      .find((b) => b.text().includes('alone'))!
      .trigger('click');
    expect(w.findAll('.name').map((n) => n.text())).toEqual(['A']);
    await w.find('form').trigger('submit');
    expect(applied(w)).toBe('A=1\n');
  });

  it('cancels without applying', async () => {
    const w = await mountForm('A=1\n');
    await w
      .findAll('button')
      .find((b) => b.text() === 'Cancel')!
      .trigger('click');
    expect(w.emitted('cancel')).toHaveLength(1);
    expect(w.emitted('apply')).toBeUndefined();
  });
});
