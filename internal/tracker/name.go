package tracker

import (
	"strings"
	"unicode"
)

// maxTitleSlug caps the title part of a ticket's name, cut at a word.
const maxTitleSlug = 40

// Name is a ticket's readable name: its key and its title in kebab case,
// e.g. JIRA-SCRUM-4 "Create website" → jira-scrum-4-create-website. It names
// the ticket's branch and pull request; the key stays its identity, since a
// title can be edited.
func Name(key, summary string) string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(summary) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	slug := ""
	for _, w := range words {
		next := w
		if slug != "" {
			next = slug + "-" + w
		}
		if len(next) > maxTitleSlug {
			if slug == "" {
				slug = w[:maxTitleSlug]
			}
			break
		}
		slug = next
	}
	name := strings.ToLower(key)
	if slug != "" {
		name += "-" + slug
	}
	return name
}

// KeyCandidates are the keys a ticket name or key may stand for, shortest
// first. A name is prefix-board-number-title and a board can itself contain
// a number segment, so every number segment after the prefix ends one
// candidate; the caller keeps the first its tracker knows.
func KeyCandidates(name string) []string {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(name)), "-")
	var out []string
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" && strings.Trim(parts[i], "0123456789") == "" {
			out = append(out, strings.Join(parts[:i+1], "-"))
		}
	}
	if len(out) == 0 {
		out = []string{strings.Join(parts, "-")}
	}
	return out
}
