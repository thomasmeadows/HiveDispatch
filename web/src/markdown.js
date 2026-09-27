// A deliberately small Markdown subset for supervisor replies: fenced code,
// inline code, bold, links and paragraphs. Input is escaped first, so the
// model's text can never inject markup.

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

function inline(s) {
  return s
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
}

export function render(text) {
  const parts = esc(text || '').split(/```[^\n]*\n?/)
  return parts
    .map((part, i) => {
      if (i % 2 === 1) return `<pre>${part.replace(/\n$/, '')}</pre>`
      return part
        .split(/\n{2,}/)
        .filter((p) => p.trim())
        .map((p) => `<p>${inline(p).replace(/\n/g, '<br>')}</p>`)
        .join('')
    })
    .join('')
}
