import { createRouter, createWebHashHistory } from 'vue-router'
import Dashboard from './views/Dashboard.vue'
import Repos from './views/Repos.vue'
import RepoDetail from './views/RepoDetail.vue'
import Configuration from './views/Configuration.vue'

export default createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: Dashboard },
    { path: '/repos', name: 'repos', component: Repos },
    { path: '/repos/detail', name: 'repo', component: RepoDetail, props: (r) => ({ path: r.query.path }) },
    { path: '/config', name: 'config', component: Configuration },
  ],
})
