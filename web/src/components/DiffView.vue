<script setup>
import { computed } from 'vue'

// Renders the server's line diff: "+" added, "-" removed, " " context,
// "---"/"+++" headers. Anything after the diff (problems, the question) is
// shown as plain text below it.
const props = defineProps({ text: { type: String, default: '' } })

const lines = computed(() =>
  props.text.replace(/\n$/, '').split('\n').map((l) => {
    let kind = 'ctx'
    if (l.startsWith('+++') || l.startsWith('---')) kind = 'head'
    else if (l.startsWith('+')) kind = 'add'
    else if (l.startsWith('-')) kind = 'del'
    return { l, kind }
  }),
)
</script>

<template>
  <pre class="diff"><span v-for="(x, i) in lines" :key="i" :class="x.kind">{{ x.l }}
</span></pre>
</template>

<style scoped>
.diff { padding: 8px 0; max-height: 50vh; }
.diff span { display: block; padding: 0 12px; }
.add { background: var(--diff-add); }
.del { background: var(--diff-del); }
.head { color: var(--muted); }
</style>
