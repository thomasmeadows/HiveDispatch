<script setup>
import { ref } from 'vue'
import ChatSidebar from './components/ChatSidebar.vue'

const chatOpen = ref(window.innerWidth > 1100)
const nav = [
  { to: '/', label: 'Dashboard', icon: 'M3 13h8V3H3zm0 8h8v-6H3zm10 0h8V11h-8zm0-18v6h8V3z' },
  { to: '/repos', label: 'Repos', icon: 'M4 4h6l2 2h8v12H4z' },
  { to: '/config', label: 'Configuration', icon: 'M12 8a4 4 0 100 8 4 4 0 000-8zm8.9 5a7 7 0 000-2l2-1.6-2-3.4-2.4 1a7 7 0 00-1.7-1L16.5 3h-4l-.4 2.6a7 7 0 00-1.7 1l-2.4-1-2 3.4 2 1.6a7 7 0 000 2l-2 1.6 2 3.4 2.4-1a7 7 0 001.7 1l.4 2.4h4l.4-2.6a7 7 0 001.7-1l2.4 1 2-3.4z' },
]
</script>

<template>
  <div class="shell" :class="{ 'chat-closed': !chatOpen }">
    <nav class="nav">
      <div class="brand">
        <svg viewBox="0 0 32 32" width="22" height="22" aria-hidden="true"><path fill="currentColor" d="M16 2l12 7v14l-12 7-12-7V9z" /></svg>
        <span>HiveDispatch</span>
      </div>
      <router-link v-for="n in nav" :key="n.to" :to="n.to" class="nav-link" :exact-active-class="n.to === '/' ? 'active' : ''" :active-class="n.to === '/' ? '' : 'active'">
        <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path fill="currentColor" :d="n.icon" /></svg>
        <span>{{ n.label }}</span>
      </router-link>
      <div class="spacer" />
      <button class="chat-toggle" @click="chatOpen = !chatOpen">{{ chatOpen ? 'Hide' : 'Show' }} supervisor</button>
    </nav>
    <main class="main">
      <router-view />
    </main>
    <ChatSidebar v-show="chatOpen" class="chat" @close="chatOpen = false" />
    <button v-if="!chatOpen" class="chat-fab primary" @click="chatOpen = true">Supervisor</button>
  </div>
</template>

<style scoped>
.shell {
  display: grid;
  grid-template-columns: 210px minmax(0, 1fr) 380px;
  height: 100%;
}
.shell.chat-closed { grid-template-columns: 210px minmax(0, 1fr); }

.nav {
  background: var(--sidebar);
  color: var(--sidebar-fg);
  display: flex;
  flex-direction: column;
  padding: 16px 10px;
  gap: 2px;
}
.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 700;
  font-size: 16px;
  padding: 4px 10px 18px;
  color: var(--accent);
}
.brand span { color: var(--sidebar-fg); }
.nav-link {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  color: var(--sidebar-muted);
  text-decoration: none;
}
.nav-link:hover { color: var(--sidebar-fg); background: rgba(255, 255, 255, .05); }
.nav-link.active { color: var(--sidebar-fg); background: rgba(224, 165, 38, .16); }
.nav-link.active svg { color: var(--accent); }
.chat-toggle {
  background: transparent;
  color: var(--sidebar-muted);
  border-color: rgba(255, 255, 255, .12);
}

.main {
  overflow-y: auto;
  padding: 28px 32px 48px;
}

.chat { border-left: 1px solid var(--line); min-height: 0; }

.chat-fab {
  position: fixed;
  right: 20px;
  bottom: 20px;
  z-index: 10;
  border-radius: 99px;
  padding: 10px 18px;
  box-shadow: 0 4px 14px rgba(0, 0, 0, .2);
}

@media (max-width: 1100px) {
  .shell, .shell.chat-closed { grid-template-columns: 64px minmax(0, 1fr); }
  .nav { padding: 12px 8px; }
  .brand span, .nav-link span { display: none; }
  .brand { padding: 4px 11px 18px; }
  .chat-toggle { display: none; }
  .chat {
    position: fixed;
    right: 0;
    top: 0;
    bottom: 0;
    width: min(380px, 100vw);
    z-index: 20;
    box-shadow: -8px 0 24px rgba(0, 0, 0, .2);
  }
}

@media (max-width: 640px) {
  .shell, .shell.chat-closed { grid-template-columns: 1fr; grid-template-rows: auto 1fr; }
  .nav { flex-direction: row; align-items: center; padding: 8px 12px; }
  .brand { padding: 0 8px 0 0; }
  .main { padding: 20px 16px 40px; }
}
</style>
