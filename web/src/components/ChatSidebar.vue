<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { api } from '../api.js'
import { render } from '../markdown.js'
import DiffView from './DiffView.vue'

const emit = defineEmits(['close'])

const log = ref([])
const model = ref('')
const session = ref('')
const error = ref('')
const busy = ref(false)
const thinking = ref(false)
const draft = ref('')
const sendError = ref('')
const scroller = ref(null)
let lastSeq = 0
let source = null

// answered maps a confirm id to true/false once confirm_done arrives.
const answered = computed(() => {
  const m = {}
  for (const e of log.value) if (e.type === 'confirm_done') m[e.id] = e.approved
  return m
})

// shown is the log without bookkeeping events, with each tool call folded
// into one line that gains its result when tool_done arrives.
const shown = computed(() => {
  const out = []
  for (const e of log.value) {
    if (e.type === 'thinking' || e.type === 'confirm_done' || e.type === 'reset') continue
    if (e.type === 'tool_done') {
      for (let i = out.length - 1; i >= 0; i--) {
        if (out[i].type === 'tool_start' && out[i].tool === e.tool && !out[i].done) {
          out[i] = { ...out[i], done: true, result: e.text, is_error: e.is_error }
          break
        }
      }
      continue
    }
    out.push(e)
  }
  return out
})

function apply(e) {
  if (e.seq <= lastSeq) return
  lastSeq = e.seq
  if (e.type === 'reset') log.value = []
  if (e.type === 'thinking') thinking.value = e.busy
  if (e.type === 'user') busy.value = true
  if (e.type === 'reply' || e.type === 'error') {
    busy.value = false
    thinking.value = false
  }
  log.value.push(e)
  scrollDown()
}

async function load() {
  try {
    const st = await api.chat()
    model.value = st.model || ''
    session.value = st.session || ''
    error.value = st.error || ''
    busy.value = st.busy
    log.value = []
    lastSeq = 0
    for (const e of st.log) apply(e)
  } catch (e) {
    error.value = e.message
  }
}

function connect() {
  source = api.events()
  source.onmessage = (m) => apply(JSON.parse(m.data))
  // EventSource reconnects by itself; reload the state to fill any gap.
  source.onopen = () => load()
}

async function send() {
  const msg = draft.value.trim()
  if (!msg || busy.value) return
  sendError.value = ''
  try {
    await api.send(msg)
    draft.value = ''
  } catch (e) {
    sendError.value = e.message
  }
}

function onKey(ev) {
  if (ev.key === 'Enter' && !ev.shiftKey) {
    ev.preventDefault()
    send()
  }
}

async function answer(id, approve) {
  try {
    await api.confirm(id, approve)
  } catch (e) {
    sendError.value = e.message
  }
}

async function reset() {
  sendError.value = ''
  try {
    const st = await api.reset()
    model.value = st.model || ''
    session.value = st.session || ''
    error.value = st.error || ''
  } catch (e) {
    sendError.value = e.message
  }
}

async function cancel() {
  try {
    await api.cancel()
  } catch (e) {
    sendError.value = e.message
  }
}

function scrollDown() {
  nextTick(() => {
    if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
  })
}

const isDiff = (t) => t.startsWith('--- ')

onMounted(connect)
onBeforeUnmount(() => source && source.close())
</script>

<template>
  <aside class="chat-panel">
    <header>
      <div>
        <div class="title">Supervisor</div>
        <div class="muted small">{{ model || 'not connected' }}</div>
      </div>
      <div class="row">
        <button title="Start a new conversation (notes are kept)" :disabled="busy" @click="reset">New</button>
        <button title="Close" @click="emit('close')">✕</button>
      </div>
    </header>

    <div ref="scroller" class="log">
      <div v-if="error" class="notice bad">
        {{ error }}
        <div class="small">Set a provider on the Configuration page (Supervisor tab), then <button class="link" @click="reset">retry</button>.</div>
      </div>
      <div v-else-if="!shown.length" class="empty muted">
        Ask about setup, a failing check, or a change to your config. The supervisor can read your config and docs, and asks
        before it changes anything.
      </div>

      <template v-for="e in shown" :key="e.seq">
        <div v-if="e.type === 'user'" class="msg user">{{ e.text }}</div>
        <div v-else-if="e.type === 'reply'" class="msg reply" v-html="render(e.text)" />
        <div v-else-if="e.type === 'error'" class="notice bad small">{{ e.text }}</div>
        <div v-else-if="e.type === 'budget'" class="notice warn small">Step budget reached — say “continue” to keep going.</div>
        <details v-else-if="e.type === 'tool_start'" class="tool">
          <summary>
            <span :class="['dot', e.done ? (e.is_error ? 'bad' : 'ok') : 'run']" />
            <code>{{ e.tool }}</code> <span class="muted args">{{ e.args === '{}' ? '' : e.args }}</span>
          </summary>
          <pre v-if="e.done">{{ e.result }}</pre>
          <div v-else class="muted small">running…</div>
        </details>
        <div v-else-if="e.type === 'confirm'" class="confirm" :class="{ decided: e.id in answered }">
          <div class="confirm-title">The supervisor wants to make a change</div>
          <DiffView v-if="isDiff(e.text)" :text="e.text" />
          <pre v-else>{{ e.text }}</pre>
          <div v-if="e.id in answered" class="small" :class="answered[e.id] ? 'ok' : 'muted'">
            {{ answered[e.id] ? 'Approved' : 'Declined' }}
          </div>
          <div v-else class="row">
            <button class="primary" @click="answer(e.id, true)">Approve</button>
            <button @click="answer(e.id, false)">Decline</button>
          </div>
        </div>
      </template>

      <div v-if="busy" class="thinking muted small">
        <span class="pulse" /> {{ thinking ? 'thinking…' : 'working…' }}
        <button class="link" @click="cancel">stop</button>
      </div>
    </div>

    <footer>
      <div v-if="sendError" class="notice bad small">{{ sendError }}</div>
      <textarea
        v-model="draft"
        rows="3"
        :disabled="!!error"
        placeholder="Message the supervisor (Enter to send, Shift+Enter for a new line)"
        @keydown="onKey"
      />
      <div class="row">
        <span class="muted small">{{ session }}</span>
        <div class="spacer" />
        <button class="primary" :disabled="busy || !draft.trim() || !!error" @click="send">Send</button>
      </div>
    </footer>
  </aside>
</template>

<style scoped>
.chat-panel {
  display: flex;
  flex-direction: column;
  background: var(--panel);
  height: 100%;
  min-width: 0;
}
header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  border-bottom: 1px solid var(--line);
}
.title { font-weight: 650; }
.small { font-size: 12px; }
.log {
  flex: 1;
  overflow-y: auto;
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.empty { font-size: 13px; }
.msg { border-radius: 10px; padding: 8px 12px; overflow-wrap: anywhere; }
.msg.user {
  align-self: flex-end;
  background: var(--accent-soft);
  max-width: 88%;
  white-space: pre-wrap;
}
.msg.reply { background: var(--bg); }
.msg.reply :deep(p) { margin: 0 0 8px; }
.msg.reply :deep(p:last-child) { margin-bottom: 0; }
.msg.reply :deep(pre) { margin: 6px 0; }
.msg.reply :deep(code) { background: var(--code-bg); padding: 0 3px; border-radius: 3px; }
.tool { font-size: 12px; }
.tool summary { cursor: pointer; list-style: none; display: flex; gap: 6px; align-items: baseline; min-width: 0; }
.tool summary::-webkit-details-marker { display: none; }
.tool .args { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; font-family: var(--mono); }
.tool pre { margin-top: 6px; max-height: 240px; }
.dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; flex: none; }
.dot.ok { background: var(--ok); }
.dot.bad { background: var(--bad); }
.dot.run { background: var(--accent); }
.confirm {
  border: 1px solid var(--accent);
  border-radius: 8px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: var(--accent-soft);
}
.confirm.decided { border-color: var(--line); background: transparent; }
.confirm-title { font-weight: 600; font-size: 13px; }
.confirm pre, .confirm :deep(pre) { background: var(--panel); }
.ok { color: var(--ok); }
.thinking { display: flex; align-items: center; gap: 6px; }
.pulse {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--accent);
  animation: pulse 1s ease-in-out infinite alternate;
}
@keyframes pulse { from { opacity: .25; } to { opacity: 1; } }
footer {
  border-top: 1px solid var(--line);
  padding: 12px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
