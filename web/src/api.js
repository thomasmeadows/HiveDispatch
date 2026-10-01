// Thin wrappers over the hivedispatch website API. Every call resolves to
// the decoded JSON or throws an Error carrying the server's message.

async function call(method, path, body) {
  const init = { method, headers: {} }
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const res = await fetch(path, init)
  let data = null
  try {
    data = await res.json()
  } catch {
    // an empty or non-JSON body; the status says enough
  }
  if (!res.ok) {
    throw new Error((data && data.error) || `${res.status} ${res.statusText}`)
  }
  return data
}

const q = (path) => (path ? `?path=${encodeURIComponent(path)}` : '')

export const api = {
  overview: () => call('GET', '/api/overview'),
  check: () => call('GET', '/api/check'),
  runs: () => call('GET', '/api/runs'),
  repos: () => call('GET', '/api/repos'),
  enrol: (path, tracker) => call('POST', '/api/repos/enrol', { path, tracker }),
  setup: (path) => call('POST', '/api/repos/setup', { path }),
  agents: (path) => call('GET', `/api/repos/agents${q(path)}`),
  saveAgents: (path, agents) => call('POST', `/api/repos/agents${q(path)}`, { agents }),
  executors: () => call('GET', '/api/executors'),
  installExecutor: (name) => call('POST', '/api/executors/install', { name }),
  getFile: (kind, path) => call('GET', `/api/files/${kind}${q(path)}`),
  // edit is { content } or { set }; apply=false previews.
  saveFile: (kind, path, edit, apply) => call('POST', `/api/files/${kind}${q(path)}`, { ...edit, apply }),
  chat: () => call('GET', '/api/chat'),
  send: (message) => call('POST', '/api/chat', { message }),
  confirm: (id, approve) => call('POST', '/api/chat/confirm', { id, approve }),
  cancel: () => call('POST', '/api/chat/cancel', {}),
  reset: () => call('POST', '/api/chat/reset', {}),
  events: () => new EventSource('/api/chat/events'),
}
