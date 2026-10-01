<script setup>
import { computed, onMounted, ref } from 'vue'
import { api } from '../api.js'

const rows = ref(null)
const error = ref('')
const loading = ref(false)
const confirming = ref(null) // the row whose install awaits confirmation
const installing = ref('') // name of the executor being installed
const results = ref({}) // name -> install result

async function load() {
  loading.value = true
  error.value = ''
  try {
    rows.value = (await api.executors()).executors
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

async function install(row) {
  confirming.value = null
  installing.value = row.name
  try {
    const res = await api.installExecutor(row.name)
    results.value = { ...results.value, [row.name]: res }
    const i = rows.value.findIndex((r) => r.name === row.name)
    if (i >= 0) rows.value[i] = res.executor
  } catch (e) {
    results.value = { ...results.value, [row.name]: { error: e.message } }
  } finally {
    installing.value = ''
  }
}

const missingUsed = computed(() => (rows.value || []).filter((r) => !r.installed && r.used_by.length))
const state = (r) => (r.installed ? (r.problem ? 'warn' : 'ok') : r.used_by.length ? 'bad' : '')

onMounted(load)
</script>

<template>
  <div>
    <h1>Agent Configuration</h1>
    <p class="subtitle">
      The coding-agent CLIs this worker can run. Each one is checked with <code>--version</code>; install the missing
      ones here. Which agents use which CLI is set per repository under Repos → Agents, and binary paths under
      Configuration.
    </p>
    <div v-if="error" class="notice bad">{{ error }}</div>
    <div v-if="missingUsed.length" class="notice bad">
      Agents are set to use {{ missingUsed.map((r) => r.label).join(', ') }}, which {{ missingUsed.length === 1 ? 'is' : 'are' }}
      not installed. Their tickets will fail until you install {{ missingUsed.length === 1 ? 'it' : 'them' }}.
    </div>

    <div class="toolbar">
      <div class="spacer" />
      <button :disabled="loading || !!installing" @click="load">{{ loading ? 'Checking…' : 'Check again' }}</button>
    </div>

    <p v-if="!rows && loading" class="muted">Checking…</p>

    <div v-for="r in rows || []" :key="r.name" class="card">
      <div class="card-head">
        <h2>{{ r.label }}</h2>
        <span v-if="r.installed" class="badge" :class="state(r)">installed</span>
        <span v-else class="badge" :class="state(r) || 'muted-badge'">not installed</span>
        <div class="spacer" />
        <button
          v-if="!r.installed && r.install"
          class="primary"
          :disabled="!!installing"
          @click="confirming = r"
        >{{ installing === r.name ? 'Installing…' : 'Install' }}</button>
        <a v-else-if="!r.installed" :href="r.docs" target="_blank" rel="noopener">Install guide →</a>
      </div>

      <dl class="facts">
        <dt>Binary</dt>
        <dd><code>{{ r.binary }}</code> <span class="muted">({{ r.config_key }})</span></dd>
        <template v-if="r.installed">
          <dt>Version</dt>
          <dd class="mono">{{ r.version || '(printed nothing)' }}</dd>
          <dt>Path</dt>
          <dd class="mono">{{ r.path }}</dd>
        </template>
        <template v-else-if="r.install">
          <dt>Install with</dt>
          <dd><code>{{ r.install }}</code></dd>
        </template>
        <dt>Used by</dt>
        <dd>
          <span v-if="r.used_by.length">{{ r.used_by.join(', ') }}</span>
          <span v-else class="muted">no agent</span>
        </dd>
      </dl>
      <div v-if="r.problem" class="notice warn">{{ r.problem }}</div>
      <p class="muted note">{{ r.note }} <a :href="r.docs" target="_blank" rel="noopener">Docs</a></p>

      <template v-if="results[r.name]">
        <div v-if="results[r.name].error" class="notice bad">{{ results[r.name].error }}</div>
        <details v-else :open="results[r.name].exit_code !== 0 || !r.installed">
          <summary>
            Install output
            <span class="badge" :class="results[r.name].exit_code === 0 ? 'ok' : 'bad'">exit {{ results[r.name].exit_code }}</span>
          </summary>
          <pre>{{ results[r.name].output || '(no output)' }}</pre>
        </details>
        <div v-if="!results[r.name].error && results[r.name].exit_code === 0 && !r.installed" class="notice warn">
          The installer finished, but <code>{{ r.binary }} --version</code> still fails. The binary may be somewhere
          not on the worker's PATH: set <code>{{ r.config_key }}</code> under Configuration to its full path.
        </div>
      </template>
    </div>

    <div v-if="confirming" class="modal-back" @click.self="confirming = null">
      <div class="modal">
        <h2>Install {{ confirming.label }}?</h2>
        <p>This runs the following command on this worker, as the user running <code>hivedispatch website</code>:</p>
        <pre>{{ confirming.install }}</pre>
        <p class="muted">{{ confirming.note }}</p>
        <div class="row">
          <div class="spacer" />
          <button @click="confirming = null">Cancel</button>
          <button class="primary" @click="install(confirming)">Install</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { display: flex; margin-bottom: 12px; }
.card-head { gap: 10px; }
.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 16px;
  margin: 8px 0;
}
.facts dt { color: var(--muted); }
.facts dd { margin: 0; overflow-wrap: anywhere; }
.note { margin: 8px 0 0; }
.muted-badge { opacity: .7; }
details { margin-top: 10px; }
summary { cursor: pointer; display: flex; gap: 8px; align-items: center; }
</style>
