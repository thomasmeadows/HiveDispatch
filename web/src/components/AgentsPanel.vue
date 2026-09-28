<script setup>
import { computed, ref, watch } from 'vue'
import { api } from '../api.js'

// A repository's agents: a pool that each works one ticket at a time. The
// list is saved whole to .hive-dispatch/agents.yaml on every change.
const props = defineProps({ path: { type: String, required: true } })

const data = ref(null) // { path, exists, agents, problem }
const error = ref('')
const busy = ref(false)
const modal = ref(null) // { index: -1 for new, form: { name, executor, model }, error }

const executors = ['claude', 'codex', 'fake']
const agents = computed(() => (data.value ? data.value.agents : []))

async function load() {
  error.value = ''
  try {
    data.value = await api.agents(props.path)
  } catch (e) {
    error.value = e.message
  }
}

async function save(list) {
  busy.value = true
  try {
    data.value = await api.saveAgents(props.path, list)
    return ''
  } catch (e) {
    return e.message
  } finally {
    busy.value = false
  }
}

function openNew() {
  modal.value = { index: -1, form: { name: uniqueName('agent'), executor: 'claude', model: '' }, error: '' }
}

function openEdit(i) {
  modal.value = { index: i, form: { ...agents.value[i] }, error: '' }
}

// uniqueName adds (or bumps) a -N suffix until no agent has the name:
// claude-1 → claude-2, default → default-2.
function uniqueName(name) {
  const taken = new Set(agents.value.map((a) => a.name.toLowerCase()))
  if (!taken.has(name.toLowerCase())) return name
  const m = name.match(/^(.*)-(\d+)$/)
  const base = m ? m[1] : name
  let n = m ? Number(m[2]) + 1 : 2
  while (taken.has(`${base}-${n}`.toLowerCase())) n++
  return `${base}-${n}`
}

async function duplicate(i) {
  const a = agents.value[i]
  const list = [...agents.value]
  list.splice(i + 1, 0, { ...a, name: uniqueName(a.name) })
  error.value = await save(list)
}

async function submit() {
  const f = modal.value.form
  const agent = { name: f.name.trim(), executor: f.executor, model: (f.model || '').trim() }
  const list = [...agents.value]
  if (modal.value.index < 0) list.push(agent)
  else list[modal.value.index] = agent
  const err = await save(list)
  if (err) modal.value.error = err
  else modal.value = null
}

async function remove() {
  const list = agents.value.filter((_, i) => i !== modal.value.index)
  const err = await save(list)
  if (err) modal.value.error = err
  else modal.value = null
}

watch(() => props.path, load, { immediate: true })
</script>

<template>
  <div>
    <div v-if="error" class="notice bad">{{ error }}</div>
    <template v-if="data">
      <div class="row head">
        <p class="muted intro">
          Agents work this repository’s tickets as a pool: each takes one ticket at a time, up to the worker’s
          <em>Tickets at once</em>. Label a ticket <code>hive:agent:&lt;name&gt;</code> to have that agent work it.
          All agents follow the Agent Policies tab; a model set here overrides the policy’s.
        </p>
        <div class="spacer" />
        <button class="primary" :disabled="busy" @click="openNew">New agent</button>
      </div>
      <div v-if="data.problem" class="notice bad">
        {{ data.problem }}<br />Saving a list here replaces the broken one.
      </div>
      <div v-if="!data.exists" class="notice warn">
        No <code>agents.yaml</code> yet: this repository uses one default Claude agent. Any change here creates the file.
      </div>

      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Name</th><th>Executor</th><th>Model</th><th>Pin label</th><th /></tr>
          </thead>
          <tbody>
            <tr v-for="(a, i) in agents" :key="a.name">
              <td><button class="link name" @click="openEdit(i)">{{ a.name }}</button></td>
              <td><span class="badge">{{ a.executor }}</span></td>
              <td>
                <span v-if="a.model">{{ a.model }}</span>
                <span v-else class="muted">policy default</span>
              </td>
              <td><code>hive:agent:{{ a.name }}</code></td>
              <td class="actions"><button :disabled="busy" @click="duplicate(i)">Duplicate</button></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="muted small">
        Saved to <code>{{ data.path }}</code>, read from this checkout — commit it so other clones have the same agents.
      </p>
    </template>

    <div v-if="modal" class="modal-back" @click.self="modal = null">
      <form class="modal" @submit.prevent="submit">
        <h2>{{ modal.index < 0 ? 'New agent' : `Edit ${agents[modal.index].name}` }}</h2>
        <label class="field">
          <span class="label">Name</span>
          <input v-model="modal.form.name" required autofocus />
          <span class="help muted">Letters, digits, <code>.</code>, <code>-</code> or <code>_</code>. Used in the pin label.</span>
        </label>
        <label class="field">
          <span class="label">Executor</span>
          <select v-model="modal.form.executor">
            <option v-for="x in executors" :key="x" :value="x">{{ x }}</option>
          </select>
          <span class="help muted">
            claude runs Claude Code, codex runs Codex; fake does nothing (for trying the pipeline).
          </span>
        </label>
        <label class="field">
          <span class="label">Model</span>
          <input v-model="modal.form.model" placeholder="policy default" />
          <span class="help muted">Optional. Overrides the model in the Agent Policies tab for this agent.</span>
        </label>
        <div v-if="modal.error" class="notice bad">{{ modal.error }}</div>
        <div class="row">
          <button v-if="modal.index >= 0" type="button" class="danger" :disabled="busy || agents.length < 2" :title="agents.length < 2 ? 'A repository needs at least one agent' : ''" @click="remove">Delete</button>
          <div class="spacer" />
          <button type="button" @click="modal = null">Cancel</button>
          <button type="submit" class="primary" :disabled="busy">{{ modal.index < 0 ? 'Create' : 'Save' }}</button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.head { align-items: flex-start; margin-bottom: 12px; }
.intro { margin: 0; max-width: 640px; font-size: 13px; }
.name { font-weight: 600; font-size: 14px; }
.actions { text-align: right; }
.small { font-size: 12px; margin: 12px 0 0; overflow-wrap: anywhere; }
.field { display: flex; flex-direction: column; gap: 4px; }
.label { font-weight: 600; font-size: 13px; }
.help { font-size: 12px; }
.modal-back {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, .45);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  z-index: 30;
}
.modal {
  background: var(--panel);
  border-radius: 10px;
  width: min(480px, 100%);
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.modal h2 { margin: 0; }
</style>
