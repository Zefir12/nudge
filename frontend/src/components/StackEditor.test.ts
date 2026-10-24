import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vite-plus/test';

import { stubApi } from '../test-utils';
import StackEditor from './StackEditor.vue';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const mountEditor = (compose: string, env = '') =>
  mount(StackEditor, {
    props: {
      compose,
      env,
      'onUpdate:compose': (v: string) => void wrapper.setProps({ compose: v }),
      'onUpdate:env': (v: string) => void wrapper.setProps({ env: v }),
    },
  });
let wrapper: ReturnType<typeof mountEditor>;

const button = () => wrapper.findAll('button').find((b) => b.text().includes('Check'))!;
const errorList = (label: string) => wrapper.find(`[aria-label="${label}"]`);

describe('StackEditor', () => {
  it('shows YAML errors while typing', async () => {
    wrapper = mountEditor('services:\n  web: {}\n');
    expect(errorList('YAML errors').exists()).toBe(false);
    await wrapper.findAll('textarea')[0]!.setValue('services:\n  web: {}\n web: {}\n');
    expect(errorList('YAML errors').exists()).toBe(true);
    expect(button().attributes('disabled')).toBeDefined();
    expect(await (wrapper.vm as unknown as { validate: () => Promise<boolean> }).validate()).toBe(false);
  });

  it('checks the files with compose and forgets the answer on edit', async () => {
    const fetch = stubApi({
      'POST /stacks/check': { body: { errors: ['services.web.ports must be a list'] } },
    });
    wrapper = mountEditor('services:\n  web:\n    ports: 80\n', 'A=1\n');
    await button().trigger('click');
    await flushPromises();
    expect(JSON.parse(fetch.mock.calls[0]![1]!.body as string)).toEqual({
      compose: 'services:\n  web:\n    ports: 80\n',
      env: 'A=1\n',
    });
    expect(errorList('Compose errors').text()).toContain('services.web.ports must be a list');

    await wrapper.findAll('textarea')[1]!.setValue('A=2\n');
    expect(errorList('Compose errors').exists()).toBe(false);
  });

  it.each([
    [[], undefined, true],
    [['bad'], true, true],
    [['bad'], false, false],
  ])('validates with compose errors %j (confirm %s) as %s', async (errors, confirmed, want) => {
    stubApi({ 'POST /stacks/check': { body: { errors } } });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(confirmed ?? false);
    wrapper = mountEditor('services: {}\n');
    expect(await (wrapper.vm as unknown as { validate: () => Promise<boolean> }).validate()).toBe(want);
    expect(confirm).toHaveBeenCalledTimes(confirmed === undefined ? 0 : 1);
  });

  it('does not block saving when compose cannot check', async () => {
    stubApi({ 'POST /stacks/check': { status: 503, body: { error: 'docker-compose is not installed' } } });
    wrapper = mountEditor('services: {}\n');
    expect(await (wrapper.vm as unknown as { validate: () => Promise<boolean> }).validate()).toBe(true);
    await flushPromises();
    expect(wrapper.text()).toContain('docker-compose is not installed');
  });
});
