<script setup>
import { computed, ref, watch } from 'vue'
import { api } from '../api.js'
import DiffView from './DiffView.vue'

// Edits one config file two ways: a form over the fields in schema (saved
// as a patch, so the file's comments survive) or its raw YAML. Every save
// is previewed as a diff with the loader's remaining problems first.
const props = defineProps({
  kind: { type: String, required: true }, // worker | supervisor | repo | policy
  path: { type: String, default: '' }, // repository, for repo and policy
  schema: { type: Array, required: true },
})
const emit = defineEmits(['saved'])

const tab = ref('form')
const file = ref(null)
const loadError = ref('')
const values = ref({}) // form inputs, as strings/bools
const original = ref({})
const raw = ref('')
const review = ref(null) // { edit, diff, problems, changed }
const saving = ref(false)
const saveError = ref('')
const savedNote = ref('')

const get = (obj, key) => key.split('.').reduce((o, k) => (o && typeof o === 'object' ? o[k] : undefined), obj)

const fields = computed(() => props.schema.flatMap((s) => s.fields))

function toInput(f, v) {
  if (v === undefined || v === null) return f.type === 'bool' ? false : ''
  switch (f.type) {
    case 'bool':
      return !!v
    case 'list':
      return Array.isArray(v) ? v.join('\n') : String(v)
    case 'pathlist':
      return Array.isArray(v) ? v.map((x) => (x && x.path) || '').join('\n') : ''
    default:
      return typeof v === 'object' ? JSON.stringify(v) : String(v)
  }
}

// fromInput converts a form input to what goes into the YAML; null removes
// the key so the default applies.
function fromInput(f, s) {
  switch (f.type) {
    case 'bool':
      return s ? true : null
    case 'number': {
      const t = String(s).trim()
      if (t === '') return null
      const n = Number(t)
      return Number.isFinite(n) ? n : t
    }
    case 'list':
    case 'pathlist': {
      const items = String(s).split('\n').map((x) => x.trim()).filter(Boolean)
      if (!items.length) return null
      return f.type === 'pathlist' ? items.map((path) => ({ path })) : items
    }
    default: {
      const t = f.type === 'textarea' ? String(s) : String(s).trim()
      return t.trim() === '' ? null : t
    }
  }
}

const patch = computed(() => {
  const set = {}
  for (const f of fields.value) {
    const now = JSON.stringify(fromInput(f, values.value[f.key]))
    const was = JSON.stringify(fromInput(f, original.value[f.key]))
    if (now !== was) set[f.key] = fromInput(f, values.value[f.key])
  }
  return set
})
const formDirty = computed(() => Object.keys(patch.value).length > 0)
const rawDirty = computed(() => file.value && raw.value !== (file.value.raw ?? file.value.starter ?? ''))

const visibleSections = computed(() => props.schema.filter((s) => !s.when || s.when(values.value)))

async function load() {
  loadError.value = ''
  review.value = null
  try {
    const f = await api.getFile(props.kind, props.path)
    file.value = f
    const v = {}
    for (const fd of fields.value) v[fd.key] = toInput(fd, get(f.data, fd.key))
    values.value = v
    original.value = { ...v }
    raw.value = f.exists ? f.raw : f.starter || ''
    if (f.parse_error) tab.value = 'yaml'
  } catch (e) {
    loadError.value = e.message
    file.value = null
  }
}

async function preview(edit) {
  saveError.value = ''
  savedNote.value = ''
  try {
    const r = await api.saveFile(props.kind, props.path, edit, false)
    review.value = { edit, ...r }
  } catch (e) {
    saveError.value = e.message
  }
}

async function apply() {
  saving.value = true
  saveError.value = ''
  try {
    const r = await api.saveFile(props.kind, props.path, review.value.edit, true)
    review.value = null
    savedNote.value = r.problems ? 'Saved, but the file still has problems:\n' + r.problems : 'Saved. The previous version is kept as .bak.'
    await load()
    emit('saved')
  } catch (e) {
    saveError.value = e.message
  } finally {
    saving.value = false
  }
}

function discard() {
  values.value = { ...original.value }
  raw.value = file.value ? (file.value.exists ? file.value.raw : file.value.starter || '') : ''
}

watch(() => [props.kind, props.path], load, { immediate: true })
</script>

<template>
  <div>
    <div v-if="loadError" class="notice bad">{{ loadError }}</div>
    <template v-else-if="file">
      <div class="row path">
        <code>{{ file.path }}</code>
        <span v-if="!file.exists" class="badge warn">not created yet</span>
      </div>
      <div v-if="file.parse_error" class="notice bad">This file does not parse: {{ file.parse_error }}</div>
      <div v-if="savedNote" class="notice" :class="savedNote.startsWith('Saved.') ? 'ok' : 'warn'">{{ savedNote }}</div>

      <div class="tabs">
        <button :class="{ active: tab === 'form' }" @click="tab = 'form'">Form</button>
        <button :class="{ active: tab === 'yaml' }" @click="tab = 'yaml'">YAML</button>
      </div>

      <div v-if="tab === 'form'">
        <p v-if="!file.exists" class="muted">Filling in the form creates the file. The YAML tab has a commented starter.</p>
        <section v-for="sec in visibleSections" :key="sec.title">
          <h3>{{ sec.title }}</h3>
          <p v-if="sec.note" class="muted note">{{ sec.note }}</p>
          <div class="grid">
            <label v-for="f in sec.fields" :key="f.key" :class="['field', { wide: f.type === 'textarea' || f.type === 'list' || f.type === 'pathlist' }]">
              <span class="label">{{ f.label }} <code class="key">{{ f.key }}</code></span>
              <select v-if="f.type === 'select'" v-model="values[f.key]">
                <option v-for="o in f.options" :key="o" :value="o">{{ o || `default${f.placeholder ? ' (' + f.placeholder + ')' : ''}` }}</option>
              </select>
              <span v-else-if="f.type === 'bool'" class="check"><input v-model="values[f.key]" type="checkbox" /> enabled</span>
              <textarea v-else-if="f.type === 'textarea' || f.type === 'list' || f.type === 'pathlist'" v-model="values[f.key]" :rows="f.type === 'textarea' ? 4 : 3" :placeholder="f.placeholder" />
              <input v-else v-model="values[f.key]" type="text" :inputmode="f.type === 'number' ? 'decimal' : undefined" :placeholder="f.placeholder" />
              <span v-if="f.help" class="help muted">{{ f.help }}</span>
            </label>
          </div>
        </section>
        <div class="actions row">
          <button :disabled="!formDirty" @click="discard">Discard</button>
          <button class="primary" :disabled="!formDirty" @click="preview({ set: patch })">Review changes</button>
        </div>
      </div>

      <div v-else>
        <textarea v-model="raw" class="yaml" rows="24" spellcheck="false" />
        <div class="actions row">
          <button :disabled="!rawDirty" @click="discard">Discard</button>
          <button class="primary" :disabled="!rawDirty && file.exists" @click="preview({ content: raw })">Review changes</button>
        </div>
      </div>
      <div v-if="saveError" class="notice bad">{{ saveError }}</div>
    </template>

    <div v-if="review" class="modal-back" @click.self="review = null">
      <div class="modal">
        <h2>Review changes</h2>
        <p v-if="!review.changed" class="muted">No changes to the file.</p>
        <DiffView v-else :text="review.diff" />
        <div v-if="review.problems" class="notice warn">
          <strong>The loader still reports problems</strong> (you can save anyway and fix them later):
          <br />{{ review.problems }}
        </div>
        <div v-else-if="review.changed" class="notice ok">The loader accepts this file.</div>
        <div class="row">
          <div class="spacer" />
          <button @click="review = null">Back</button>
          <button class="primary" :disabled="!review.changed || saving" @click="apply">{{ saving ? 'Saving…' : 'Save' }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.path { margin-bottom: 12px; overflow-wrap: anywhere; }
.note { margin: -4px 0 10px; font-size: 13px; }
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 14px 18px;
}
.field { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.field.wide { grid-column: 1 / -1; }
.label { font-weight: 600; font-size: 13px; }
.key { font-weight: 400; color: var(--muted); font-size: 11px; margin-left: 4px; }
.help { font-size: 12px; }
.check { display: flex; gap: 8px; align-items: center; }
.check input { width: auto; }
.actions { margin: 20px 0 8px; justify-content: flex-end; }
.yaml { min-height: 360px; line-height: 1.5; }
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
  width: min(760px, 100%);
  max-height: 90vh;
  overflow-y: auto;
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
</style>
