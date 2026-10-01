import { createRouter, createWebHashHistory } from 'vue-router'
import Dashboard from './views/Dashboard.vue'
import Repos from './views/Repos.vue'
import RepoDetail from './views/RepoDetail.vue'
import Configuration from './views/Configuration.vue'
import AgentConfiguration from './views/AgentConfiguration.vue'

export default createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: Dashboard },
    { path: '/repos', name: 'repos', component: Repos },
    { path: '/repos/detail', name: 'repo', component: RepoDetail, props: (r) => ({ path: r.query.path }) },
    { path: '/agents', name: 'agents', component: AgentConfiguration },
    { path: '/config', name: 'config', component: Configuration },
  ],
})
