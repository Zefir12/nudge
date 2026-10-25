import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import type { RegistryManifest } from '../api/client';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin, stubApi } from '../test-utils';
import RegistryRepositoryView from './RegistryRepositoryView.vue';

const registry = {
  id: 1,
  name: 'LAN',
  address: 'registry.lan:5000',
  username: 'bot',
  kind: 'generic',
  owners: [],
  plain_http: true,
  created_at: '2026-10-24T12:00:00Z',
  updated_at: '2026-10-24T12:00:00Z',
};

const digest = 'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';

const latest: RegistryManifest = {
  repository: 'team/app',
  tag: 'latest',
  digest,
  media_type: 'application/vnd.oci.image.index.v1+json',
  platforms: [
    { os: 'linux', architecture: 'amd64', digest: 'sha256:aa', size: 2048 },
    { os: 'linux', architecture: 'arm64', variant: 'v8', digest: 'sha256:bb', size: 1024 },
  ],
  shared_tags: ['v1.2'],
  shared_tags_complete: false,
};

const base = '/registries/1';
const tagsPath = `${base}/tags?repository=team%2Fapp`;
const manifestPath = (tag: string) => `${base}/manifest?repository=team%2Fapp&tag=${tag}`;

async function mountView() {
  const router = createAppRouter(createMemoryHistory());
  await router.push('/registries/1/repository?name=team/app');
  const wrapper = mount(RegistryRepositoryView, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

const tagButton = (wrapper: ReturnType<typeof mount>, tag: string) =>
  wrapper.findAll('button.tag').find((b) => b.text().endsWith(tag))!;
const deleteButton = (wrapper: ReturnType<typeof mount>) =>
  wrapper.findAll('button').find((b) => b.text() === 'Delete');

beforeEach(() => {
  currentUser.value = admin;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  currentUser.value = null;
});

describe('RegistryRepositoryView', () => {
  it('lists tags and fetches a manifest only when a tag opens', async () => {
    const fetch = stubApi({
      [`GET ${base}`]: { body: registry },
      [`GET ${tagsPath}`]: { body: [{ tag: 'latest' }, { tag: 'v1.2' }] },
      [`GET ${manifestPath('latest')}`]: { body: latest },
    });
    const wrapper = await mountView();
    expect(wrapper.find('h1').text()).toBe('team/app');
    expect(wrapper.findAll('button.tag')).toHaveLength(2);
    const manifestCalls = () => fetch.mock.calls.filter(([url]) => String(url).includes('/manifest')).length;
    expect(manifestCalls()).toBe(0);

    await tagButton(wrapper, 'latest').trigger('click');
    await flushPromises();
    expect(manifestCalls()).toBe(1);
    const detail = wrapper.find('dl').text();
    expect(detail).toContain('0123456789ab');
    expect(detail).toContain('linux/amd64');
    expect(detail).toContain('linux/arm64/v8');
    expect(detail).toContain('v1.2');

    // Closing and reopening reuses the manifest.
    await tagButton(wrapper, 'latest').trigger('click');
    await tagButton(wrapper, 'latest').trigger('click');
    await flushPromises();
    expect(manifestCalls()).toBe(1);
  });

  it('deletes a tag after a confirm naming the tags that go with it', async () => {
    let tags = [{ tag: 'latest' }, { tag: 'v1.2' }];
    const deletes: string[] = [];
    stubApi({
      [`GET ${base}`]: { body: registry },
      [`GET ${tagsPath}`]: () => ({ body: tags }),
      [`GET ${manifestPath('latest')}`]: { body: latest },
      [`DELETE ${base}/tags?repository=team%2Fapp&tag=latest&digest=${encodeURIComponent(digest)}`]: () => {
        deletes.push('latest');
        tags = [];
        return { status: 204 };
      },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const wrapper = await mountView();
    await tagButton(wrapper, 'latest').trigger('click');
    await flushPromises();

    await deleteButton(wrapper)!.trigger('click');
    await flushPromises();
    expect(deletes).toEqual([]);
    const text = confirm.mock.calls[0]![0] as string;
    expect(text).toContain('"team/app:latest"');
    expect(text).toContain('deleted with it: v1.2');
    expect(text).toContain('Not every tag was checked');

    await deleteButton(wrapper)!.trigger('click');
    await flushPromises();
    expect(deletes).toEqual(['latest']);
    expect(wrapper.text()).toContain('No tags.');
  });

  it("doesn't list shared tags for Forgejo, which deletes only the tag", async () => {
    stubApi({
      [`GET ${base}`]: { body: { ...registry, kind: 'forgejo' } },
      [`GET ${tagsPath}`]: { body: [{ tag: 'latest' }] },
      [`GET ${manifestPath('latest')}`]: { body: latest },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const wrapper = await mountView();
    await tagButton(wrapper, 'latest').trigger('click');
    await flushPromises();
    await deleteButton(wrapper)!.trigger('click');
    expect(confirm.mock.calls[0]![0]).toBe('Delete the tag "team/app:latest"? This cannot be undone.');
  });

  it('hides Delete from users who are not admins', async () => {
    currentUser.value = { ...admin, admin: false };
    stubApi({
      [`GET ${base}`]: { body: registry },
      [`GET ${tagsPath}`]: { body: [{ tag: 'latest' }] },
      [`GET ${manifestPath('latest')}`]: { body: latest },
    });
    const wrapper = await mountView();
    await tagButton(wrapper, 'latest').trigger('click');
    await flushPromises();
    expect(wrapper.find('dl').exists()).toBe(true);
    expect(deleteButton(wrapper)).toBeUndefined();
  });

  it('shows a failed delete', async () => {
    stubApi({
      [`GET ${base}`]: { body: registry },
      [`GET ${tagsPath}`]: { body: [{ tag: 'latest' }] },
      [`GET ${manifestPath('latest')}`]: { body: latest },
      [`DELETE ${base}/tags?repository=team%2Fapp&tag=latest&digest=${encodeURIComponent(digest)}`]: {
        status: 409,
        body: { error: "registry doesn't allow deletes" },
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const wrapper = await mountView();
    await tagButton(wrapper, 'latest').trigger('click');
    await flushPromises();
    await deleteButton(wrapper)!.trigger('click');
    await flushPromises();
    expect(wrapper.find('[role=alert]').text()).toBe("registry doesn't allow deletes");
  });
});
