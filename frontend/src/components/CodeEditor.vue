<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue';

import { highlightEnv, highlightYaml } from '../highlight';

// CodeEditor is a textarea with syntax highlighting: the text is typed into a
// transparent textarea laid over a <pre> that shows the same text in colour. The
// textarea stays the real control, so v-model, focus, undo and form behaviour are the
// browser's own. Other attributes (aria-*, name) go to the textarea.
defineOptions({ inheritAttrs: false });

const text = defineModel<string>({ required: true });
const props = defineProps<{
  language: 'yaml' | 'env';
  // tall fills most of the viewport (the compose file), short a quarter of it.
  size?: 'tall' | 'short';
  disabled?: boolean;
}>();

const tokens = computed(() => (props.language === 'yaml' ? highlightYaml : highlightEnv)(text.value));
const pre = useTemplateRef<HTMLElement>('pre');
const area = useTemplateRef<HTMLTextAreaElement>('area');

// Scrollbars the textarea shows shrink its client box and so its scroll range. The
// <pre> shows the same (invisible) bars, so both clamp scrolling at the same offset
// and the colours stay under the text at the end of the range.
const vbar = ref(false);
const hbar = ref(false);

function measure() {
  const a = area.value;
  if (!a) return;
  vbar.value = a.offsetWidth - a.clientWidth - 2 * a.clientLeft > 0;
  hbar.value = a.offsetHeight - a.clientHeight - 2 * a.clientTop > 0;
}

function sync() {
  if (!pre.value || !area.value) return;
  pre.value.scrollTop = area.value.scrollTop;
  pre.value.scrollLeft = area.value.scrollLeft;
}

// After the bars change, the pre's range changes too, so copy the scroll again.
function refresh() {
  measure();
  requestAnimationFrame(sync);
}

watch(text, refresh, { flush: 'post' });

let observer: ResizeObserver | null = null;
onMounted(() => {
  measure();
  // The textarea can be resized by hand, which can add or remove its scrollbars.
  if (typeof ResizeObserver !== 'undefined' && area.value) {
    observer = new ResizeObserver(refresh);
    observer.observe(area.value);
  }
});
onBeforeUnmount(() => observer?.disconnect());
</script>

<template>
  <div class="code-editor" :class="[size ?? 'short', { disabled, vbar, hbar }]">
    <!-- The trailing newline gives an empty last line its height, as in the textarea. -->
    <pre
      ref="pre"
      class="layer"
      aria-hidden="true"
    ><span v-for="(t, i) in tokens" :key="i" :class="t.kind ? `t-${t.kind}` : undefined">{{ t.text }}</span>{{ '\n' }}</pre>
    <textarea
      ref="area"
      v-bind="$attrs"
      v-model="text"
      class="layer input"
      wrap="off"
      spellcheck="false"
      autocapitalize="off"
      autocomplete="off"
      :disabled="disabled"
      @scroll="sync"
    ></textarea>
  </div>
</template>

<style scoped>
.code-editor {
  position: relative;
  background: var(--field-bg);
  border-radius: var(--radius);
}

.code-editor.disabled {
  opacity: 0.6;
}

/* Both layers must lay text out identically, or the colours drift from the caret. */
.layer {
  margin: 0;
  padding: 0.25rem 0.5rem;
  font-family: var(--mono);
  font-size: 0.85rem;
  font-weight: 400;
  line-height: 1.5;
  letter-spacing: normal;
  tab-size: 2;
  white-space: pre;
  overflow-wrap: normal;
  border: 1px solid transparent;
  border-radius: var(--radius);
}

pre.layer {
  position: absolute;
  inset: 0;
  overflow: hidden;
  color: var(--text);
  pointer-events: none;
}

/* Same scrollbars as the textarea, with the thumb hidden: they only take the space. */
.vbar pre.layer {
  overflow-y: scroll;
}

.hbar pre.layer {
  overflow-x: scroll;
}

pre.layer::-webkit-scrollbar-thumb {
  background: transparent;
}

@supports not selector(::-webkit-scrollbar) {
  pre.layer {
    scrollbar-color: transparent transparent;
  }
}

.input {
  position: relative;
  display: block;
  width: 100%;
  color: transparent;
  caret-color: var(--text);
  background: transparent;
  border-color: var(--border-strong);
  resize: vertical;
}

.input:focus-visible {
  border-color: var(--accent-border);
}

.input::selection {
  color: transparent;
  background: var(--accent-soft);
}

/* Most of the viewport for compose, a quarter for .env; the minimums keep both usable
   on short screens. */
.tall .input {
  height: 65vh;
  min-height: 16rem;
}

.short .input {
  height: 22vh;
  min-height: 7rem;
}

.t-key {
  color: var(--code-key);
}

.t-string {
  color: var(--code-string);
}

.t-number {
  color: var(--code-number);
}

.t-literal {
  color: var(--code-literal);
}

.t-comment {
  color: var(--code-comment);
}

.t-punct {
  color: var(--code-punct);
}

.t-anchor {
  color: var(--code-anchor);
}

.t-variable {
  color: var(--code-variable);
}
</style>
