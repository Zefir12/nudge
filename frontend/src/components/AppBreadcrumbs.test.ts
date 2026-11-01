import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { environments } from '../environments';
import { createAppRouter } from '../router';
import { currentUser } from '../session';
import { admin } from '../test-utils';
import AppBreadcrumbs from './AppBreadcrumbs.vue';

beforeEach(() => {
  currentUser.value = admin;
  environments.value = [
    { id: 1, name: 'local', kind: 'local', online: true, created_at: '', last_seen_at: null },
  ];
});

describe('AppBreadcrumbs', () => {
  it.each([
    { path: '/', want: ['Dashboard'], links: [] },
    { path: '/env/1', want: ['Dashboard', 'local'], links: ['/'] },
    { path: '/env/1/containers', want: ['Dashboard', 'local', 'Containers'], links: ['/', '/env/1'] },
    {
      path: '/env/1/containers/abc',
      want: ['Dashboard', 'local', 'Containers'],
      links: ['/', '/env/1', '/env/1/containers'],
    },
    { path: '/env/9/images', want: ['Dashboard', '9', 'Images'], links: ['/', '/env/9'] },
    { path: '/registries/2', want: ['Dashboard', 'Registries'], links: ['/', '/registries'] },
    { path: '/audit', want: ['Dashboard', 'Audit log'], links: ['/'] },
  ])('$path', async ({ path, want, links }) => {
    const router = createAppRouter(createMemoryHistory());
    await router.push(path);
    const wrapper = mount(AppBreadcrumbs, { global: { plugins: [router] } });
    expect(wrapper.findAll('a, [aria-current]').map((c) => c.text())).toEqual(want);
    expect(wrapper.findAll('a').map((a) => a.attributes('href'))).toEqual(links);
  });
});
