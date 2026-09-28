<script setup>
import ConfigEditor from '../components/ConfigEditor.vue'
import { workerSchema } from '../schemas.js'
</script>

<template>
  <div>
    <h1>Configuration</h1>
    <p class="subtitle">
      Settings for this machine and its supervisor. Each repository’s ticket settings, agents and agent policies are on
      its page under Repos.
    </p>
    <div class="card">
      <ConfigEditor kind="worker" :schema="workerSchema" />
    </div>
  </div>
</template>
