<script setup>
import { computed, ref, watch } from 'vue'
import { api } from '../api.js'
import AgentsPanel from '../components/AgentsPanel.vue'
import ConfigEditor from '../components/ConfigEditor.vue'
import { policySchema, repoSchema } from '../schemas.js'

const props = defineProps({ path: { type: String, default: '' } })

const row = ref(null)
const error = ref('')
const tab = ref('repo')
const tracker = ref('github')
const busy = ref(false)
const actionError = ref('')
const setupOut = ref(null)

const base = computed(() => (props.path || '').split('/').filter(Boolean).pop() || props.path)

async function load() {
  error.value = ''
  try {
    const d = await api.repos()
    row.value = d.repos.find((r) => r.path === props.path) || null
    if (!row.value) error.value = `${props.path} is not among the repositories the scan found.`
  } catch (e) {
    error.value = e.message
  }
}

async function enrol() {
  busy.value = true
  actionError.value = ''
  try {
    await api.enrol(props.path, tracker.value)
    await load()
  } catch (e) {
    actionError.value = e.message
  } finally {
    busy.value = false
  }
}

async function setup() {
  busy.value = true
  actionError.value = ''
  setupOut.value = null
  try {
    setupOut.value = await api.setup(props.path)
    await load()
  } catch (e) {
    actionError.value = e.message
  } finally {
    busy.value = false
  }
}

watch(() => props.path, load, { immediate: true })
</script>

<template>
  <div>
    <router-link to="/repos" class="back">← Repos</router-link>
    <h1>{{ row && row.name ? row.name : base }}</h1>
    <p class="subtitle mono">{{ path }}</p>
    <div v-if="error" class="notice bad">{{ error }}</div>

    <template v-if="row">
      <div v-if="!row.enrolled" class="card">
        <h2>Enrol this repository</h2>
        <p class="muted">
          Writes <code>.hive-dispatch/repo.yaml</code> and <code>policy.yaml</code> starters here. Fill them in, commit
          both, and the worker picks the repository up.
        </p>
        <div v-if="row.legacy" class="notice warn">
          This repository still has the old <code>.hivedispatch.yaml</code>; move its keys into
          <code>.hive-dispatch/policy.yaml</code> after enrolling.
        </div>
        <div class="row">
          <label class="row">Queue in
            <select v-model="tracker" class="narrow">
              <option value="github">GitHub Issues</option>
              <option value="jira">Jira</option>
            </select>
          </label>
          <button class="primary" :disabled="busy" @click="enrol">Write starters</button>
        </div>
        <div v-if="actionError" class="notice bad top">{{ actionError }}</div>
      </div>

      <template v-else>
        <div class="row status">
          <span v-if="row.problem" class="badge bad">needs attention</span>
          <span v-else-if="!row.picked_up" class="badge warn">outside code_dirs</span>
          <span v-else class="badge ok">enrolled</span>
          <span v-if="row.project" class="muted">project <strong>{{ row.project }}</strong> · {{ row.tracker }}</span>
          <div class="spacer" />
          <button :disabled="busy || !!row.problem" :title="row.tracker === 'jira' ? 'Create the claim custom fields in Jira' : 'Create the hive:* labels on GitHub'" @click="setup">
            {{ busy ? 'Working…' : row.tracker === 'jira' ? 'Create Jira claim fields' : 'Create GitHub labels' }}
          </button>
        </div>
        <div v-if="row.problem" class="notice bad">{{ row.problem }}</div>
        <div v-if="!row.picked_up" class="notice warn">
          The worker will not pick this repository up: it is not under code_dirs and not listed under repos: in the
          worker config.
        </div>
        <div v-if="actionError" class="notice bad">{{ actionError }}</div>
        <div v-if="setupOut" class="card">
          <div class="card-head">
            <h2>hivedispatch init -{{ row.tracker }}</h2>
            <span class="badge" :class="setupOut.exit_code === 0 ? 'ok' : 'bad'">exit {{ setupOut.exit_code }}</span>
          </div>
          <pre>{{ setupOut.output }}</pre>
        </div>

        <div class="tabs">
          <button :class="{ active: tab === 'repo' }" @click="tab = 'repo'">Ticket Settings</button>
          <button :class="{ active: tab === 'agents' }" @click="tab = 'agents'">Agents</button>
          <button :class="{ active: tab === 'policy' }" @click="tab = 'policy'">Agent Policies</button>
        </div>
        <div class="card">
          <ConfigEditor v-if="tab === 'repo'" key="repo" kind="repo" :path="path" :schema="repoSchema" @saved="load" />
          <AgentsPanel v-else-if="tab === 'agents'" :path="path" />
          <template v-else>
            <div class="notice warn">
              Every agent of this repository follows this policy. The worker reads it from each ticket’s worktree, so
              edits apply once they are committed and pushed to the default branch.
            </div>
            <ConfigEditor key="policy" kind="policy" :path="path" :schema="policySchema" />
          </template>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.back { text-decoration: none; font-size: 13px; }
.subtitle.mono { overflow-wrap: anywhere; }
.narrow { width: auto; }
.status { margin-bottom: 14px; }
.top { margin-top: 12px; }
</style>
