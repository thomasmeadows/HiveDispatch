<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api.js'

const router = useRouter()
const data = ref(null)
const error = ref('')
const loading = ref(false)
const filter = ref('all')
const search = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    data.value = await api.repos()
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

function status(r) {
  if (r.enrolled && r.problem) return { text: 'needs attention', cls: 'bad' }
  if (r.enrolled && !r.picked_up) return { text: 'outside code_dirs', cls: 'warn' }
  if (r.enrolled) return { text: 'enrolled', cls: 'ok' }
  if (r.legacy) return { text: 'legacy .hivedispatch.yaml', cls: 'warn' }
  return { text: 'not enrolled', cls: '' }
}

const rows = computed(() => {
  const all = (data.value && data.value.repos) || []
  const s = search.value.toLowerCase()
  return all.filter((r) => {
    if (filter.value === 'enrolled' && !r.enrolled) return false
    if (filter.value === 'other' && r.enrolled) return false
    return !s || [r.path, r.project, r.name].some((x) => (x || '').toLowerCase().includes(s))
  })
})
const counts = computed(() => {
  const all = (data.value && data.value.repos) || []
  return { all: all.length, enrolled: all.filter((r) => r.enrolled).length }
})

const open = (r) => router.push({ name: 'repo', query: { path: r.path } })

onMounted(load)
</script>

<template>
  <div>
    <div class="row">
      <h1>Repos</h1>
      <div class="spacer" />
      <button :disabled="loading" @click="load">{{ loading ? 'Scanning…' : 'Rescan' }}</button>
    </div>
    <p class="subtitle">
      Git repositories found under
      <template v-if="data">
        <code v-for="(r, i) in data.roots" :key="r">{{ r }}{{ i < data.roots.length - 1 ? ', ' : '' }}</code>
        <span v-if="data.home_default"> (your home directory, because code_dirs is not set)</span>
      </template>
      <template v-else>your code directories</template>.
      A repository with <code>.hive-dispatch/repo.yaml</code> is enrolled.
    </p>
    <div v-if="error" class="notice bad">
      {{ error }}
      <div><router-link to="/config">Open Configuration</router-link></div>
    </div>
    <div v-if="data" class="card">
      <div class="row toolbar">
        <div class="tabs flat">
          <button :class="{ active: filter === 'all' }" @click="filter = 'all'">All {{ counts.all }}</button>
          <button :class="{ active: filter === 'enrolled' }" @click="filter = 'enrolled'">Enrolled {{ counts.enrolled }}</button>
          <button :class="{ active: filter === 'other' }" @click="filter = 'other'">Not enrolled {{ counts.all - counts.enrolled }}</button>
        </div>
        <div class="spacer" />
        <input v-model="search" class="search" placeholder="Filter by path, project or name" />
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Path</th><th>Status</th><th>Project</th><th>Tracker</th><th>Repository</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.path" class="clickable" @click="open(r)">
              <td class="mono">{{ r.path }}</td>
              <td><span class="badge" :class="status(r).cls">{{ status(r).text }}</span></td>
              <td>{{ r.project }}</td>
              <td>{{ r.tracker }}</td>
              <td>{{ r.name }}</td>
            </tr>
            <tr v-if="!rows.length"><td colspan="5" class="muted">No repositories match.</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { margin-bottom: 8px; }
.tabs.flat { border: none; margin: 0; }
.search { max-width: 280px; }
td.mono { overflow-wrap: anywhere; min-width: 220px; }
</style>
