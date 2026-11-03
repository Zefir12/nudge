import { mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import { beforeEach, describe, expect, it, vi } from 'vite-plus/test';

import { isFolded, setFolded } from '../folds';
import FoldCard from './FoldCard.vue';

function mountCard(foldId: string, forceOpen = false) {
  return mount(FoldCard, {
    props: { foldId, forceOpen },
    slots: { title: 'Runs', default: '<p class="body">run #1</p>', actions: '<a class="action">All</a>' },
    attachTo: document.body,
  });
}

const bodyShown = (wrapper: ReturnType<typeof mountCard>) => wrapper.find('.body').isVisible();

describe('FoldCard', () => {
  beforeEach(() => {
    setFolded('test.runs', false);
    setFolded('test.other', false);
  });

  it('folds to its title and actions, and unfolds', async () => {
    const wrapper = mountCard('test.runs');
    const toggle = wrapper.get('button.fold-toggle');
    expect(bodyShown(wrapper)).toBe(true);
    expect(toggle.attributes('aria-expanded')).toBe('true');
    expect(toggle.attributes('aria-label')).toBe('Fold');

    await toggle.trigger('click');
    expect(bodyShown(wrapper)).toBe(false);
    expect(wrapper.find('h2').text()).toBe('Runs');
    expect(wrapper.find('.action').exists()).toBe(true);
    expect(toggle.attributes('aria-expanded')).toBe('false');
    expect(toggle.attributes('aria-label')).toBe('Unfold');

    await toggle.trigger('click');
    expect(bodyShown(wrapper)).toBe(true);
  });

  it('folds every card with the same ID and no other', async () => {
    const one = mountCard('test.runs');
    const same = mountCard('test.runs');
    const other = mountCard('test.other');

    await one.get('button.fold-toggle').trigger('click');
    await nextTick();
    expect(bodyShown(same)).toBe(false);
    expect(bodyShown(other)).toBe(true);
    expect(bodyShown(mountCard('test.runs'))).toBe(false);
  });

  it('stays open while forced, and folds again after', async () => {
    setFolded('test.runs', true);
    const wrapper = mountCard('test.runs', true);
    expect(bodyShown(wrapper)).toBe(true);

    await wrapper.setProps({ forceOpen: false });
    expect(bodyShown(wrapper)).toBe(false);
    expect(isFolded('test.runs')).toBe(true);
  });
});

describe('folds', () => {
  beforeEach(() => {
    vi.resetModules();
    localStorage.clear();
  });

  it('remembers folded cards in this browser', async () => {
    const first = await import('../folds');
    first.setFolded('deployment.runs', true);
    first.setFolded('deployment.members', true);
    first.setFolded('deployment.members', false);

    vi.resetModules();
    const reloaded = await import('../folds');
    expect(reloaded.isFolded('deployment.runs')).toBe(true);
    expect(reloaded.isFolded('deployment.members')).toBe(false);
  });

  it.each([
    { name: 'broken JSON', stored: '{nope' },
    { name: 'not a list', stored: '{"deployment.runs":true}' },
    { name: 'not strings', stored: '[1,null]' },
  ])('starts unfolded from $name', async ({ stored }) => {
    localStorage.setItem('nudge.foldedCards', stored);
    const { isFolded: folded } = await import('../folds');
    expect(folded('deployment.runs')).toBe(false);
  });
});
