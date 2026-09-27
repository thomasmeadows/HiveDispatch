<script setup>
import { ref } from 'vue'
import ConfigEditor from '../components/ConfigEditor.vue'
import { supervisorSchema, workerSchema } from '../schemas.js'

const tab = ref('worker')
</script>

<template>
  <div>
    <h1>Configuration</h1>
    <p class="subtitle">
      Settings for this machine. Each repository’s tracker settings and agent policy are on its page under Repos.
    </p>
    <div class="tabs">
      <button :class="{ active: tab === 'worker' }" @click="tab = 'worker'">Worker</button>
      <button :class="{ active: tab === 'supervisor' }" @click="tab = 'supervisor'">Supervisor</button>
    </div>
    <div class="card">
      <ConfigEditor v-if="tab === 'worker'" key="worker" kind="worker" :schema="workerSchema" />
      <template v-else>
        <p class="muted">
          The supervisor’s own model settings. After saving, press <strong>New</strong> in the chat to use them.
        </p>
        <ConfigEditor key="supervisor" kind="supervisor" :schema="supervisorSchema" />
      </template>
    </div>
  </div>
</template>
