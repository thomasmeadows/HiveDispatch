<script setup>
import { computed, onMounted, ref } from 'vue'
import { api } from '../api.js'

const ov = ref(null)
const ovError = ref('')
const check = ref(null)
const checking = ref(false)
const runs = ref(null)
const runsError = ref('')
const runsLoading = ref(false)

async function loadOverview() {
  try {
    ov.value = await api.overview()
  } catch (e) {
    ovError.value = e.message
  }
}

async function runCheck() {
  checking.value = true
  try {
    check.value = (await api.check()).output
  } catch (e) {
    check.value = e.message
  } finally {
    checking.value = false
  }
}

async function loadRuns() {
  runsLoading.value = true
  runsError.value = ''
  try {
    runs.value = await api.runs()
  } catch (e) {
    runsError.value = e.message
  } finally {
    runsLoading.value = false
  }
}

const agentCount = computed(() => ((ov.value && ov.value.repos) || []).reduce((n, r) => n + (r.agents || []).length, 0))
const phaseClass = (p) => ({ done: 'ok', pr_opened: 'ok', blocked: 'bad', working: 'warn', claimed: 'warn', planned: 'warn' })[p] || ''
const when = (t) => (t ? new Date(t).toLocaleString() : '')

onMounted(async () => {
  await loadOverview()
  if (ov.value && ov.value.config_exists) {
    runCheck()
    if (!ov.value.config_problem) loadRuns()
  }
})
</script>

<template>
  <div>
    <h1>Dashboard</h1>
    <p class="subtitle">This worker at a glance: its config, what <code>check</code> says, and recent runs.</p>
    <div v-if="ovError" class="notice bad">{{ ovError }}</div>

    <template v-if="ov">
      <div v-if="!ov.config_exists" class="card">
        <h2>No worker config yet</h2>
        <p>
          There is no config at <code>{{ ov.config_path }}</code>. Create it on the
          <router-link to="/config">Configuration</router-link> page, or ask the supervisor on the right to set it up
          with you.
        </p>
      </div>

      <template v-else>
        <div class="stats">
          <div class="stat"><div class="label">Agent</div><div class="value">{{ ov.agent_id || '—' }}</div></div>
          <div class="stat"><div class="label">Agents</div><div class="value">{{ agentCount }}</div></div>
          <div class="stat"><div class="label">Tickets at once</div><div class="value">{{ ov.max_concurrent }}</div></div>
          <div class="stat"><div class="label">Enrolled repos</div><div class="value">{{ (ov.repos || []).length }}</div></div>
          <div class="stat"><div class="label">Poll every</div><div class="value">{{ ov.poll_interval }}</div></div>
        </div>
        <div v-if="ov.config_problem" class="notice bad">{{ ov.config_problem }}</div>

        <div class="card">
          <div class="card-head">
            <h2>Enrolled repositories</h2>
            <router-link to="/repos">All repos →</router-link>
          </div>
          <div class="table-wrap">
            <table>
              <thead><tr><th>Project</th><th>Tracker</th><th>Repository</th><th>Agents</th><th>Path</th></tr></thead>
              <tbody>
                <tr v-for="r in ov.repos" :key="r.path">
                  <td><strong>{{ r.project }}</strong></td>
                  <td>{{ r.tracker }}</td>
                  <td>{{ r.name }}</td>
                  <td>{{ (r.agents || []).map((a) => a.name).join(', ') }}</td>
                  <td class="mono"><router-link :to="{ name: 'repo', query: { path: r.path } }">{{ r.path }}</router-link></td>
                </tr>
                <tr v-if="!(ov.repos || []).length"><td colspan="5" class="muted">None yet — enrol one from Repos.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>

      <div v-if="ov.config_exists" class="card">
        <div class="card-head">
          <h2>hivedispatch check</h2>
          <button :disabled="checking" @click="runCheck">{{ checking ? 'Checking…' : 'Run again' }}</button>
        </div>
        <pre v-if="check !== null">{{ check }}</pre>
        <p v-else class="muted">Running…</p>
      </div>

      <div v-if="ov.config_exists" class="card">
        <div class="card-head">
          <h2>Recent runs</h2>
          <button :disabled="runsLoading" @click="loadRuns">{{ runsLoading ? 'Loading…' : runs ? 'Refresh' : 'Load' }}</button>
        </div>
        <div v-if="runsError" class="notice bad">{{ runsError }}</div>
        <p v-else-if="runsLoading && !runs" class="muted">Reading the state branches (this fetches each repository)…</p>
        <div v-else-if="runs" class="table-wrap">
          <table>
            <thead><tr><th>Ticket</th><th>Phase</th><th>Status</th><th>Attempts</th><th>Agent</th><th>Updated</th><th>PR</th></tr></thead>
            <tbody>
              <tr v-for="r in runs.slice(0, 50)" :key="r.ticket + r.updatedAt">
                <td><a v-if="r.url" :href="r.url" target="_blank" rel="noopener">{{ r.ticket }}</a><span v-else>{{ r.ticket }}</span></td>
                <td><span class="badge" :class="phaseClass(r.phase)">{{ r.phase }}</span></td>
                <td>{{ r.lastStatus }}</td>
                <td>{{ r.attempts }}</td>
                <td>{{ r.agent }}</td>
                <td class="nowrap">{{ when(r.updatedAt) }}</td>
                <td><a v-if="r.prUrl" :href="r.prUrl" target="_blank" rel="noopener">PR</a></td>
              </tr>
              <tr v-if="!runs.length"><td colspan="7" class="muted">No runs recorded.</td></tr>
            </tbody>
          </table>
        </div>
        <p v-else class="muted">Fix the config problems above to read run records.</p>
      </div>
    </template>
  </div>
</template>

<style scoped>
td.mono { overflow-wrap: anywhere; min-width: 220px; }
.nowrap { white-space: nowrap; }
</style>
