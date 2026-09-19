package jira

import (
	"fmt"
	"strings"
)

// adfNode is one node of an Atlassian Document Format tree.
type adfNode struct {
	Type    string         `json:"type"`
	Version int            `json:"version,omitempty"`
	Text    string         `json:"text,omitempty"`
	Content []adfNode      `json:"content,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// adfToText renders an ADF tree as plain text. Block nodes are separated by
// blank lines; inline formatting is dropped.
func adfToText(n *adfNode) string {
	if n == nil {
		return ""
	}
	var blocks []string
	for i := range n.Content {
		if b := renderBlock(&n.Content[i]); b != "" {
			blocks = append(blocks, b)
		}
	}
	return strings.Join(blocks, "\n\n")
}

func renderBlock(n *adfNode) string {
	switch n.Type {
	case "paragraph", "heading":
		return renderInline(n.Content)
	case "bulletList", "orderedList":
		var items []string
		for i := range n.Content {
			items = append(items, "- "+renderListItem(&n.Content[i]))
		}
		return strings.Join(items, "\n")
	case "codeBlock":
		lang, _ := n.Attrs["language"].(string)
		return "```" + lang + "\n" + renderInline(n.Content) + "\n```"
	case "blockquote", "panel", "expand", "nestedExpand", "tableCell", "tableHeader":
		return adfToText(n)
	case "table":
		var rows []string
		for i := range n.Content {
			var cells []string
			for j := range n.Content[i].Content {
				cells = append(cells, strings.ReplaceAll(adfToText(&n.Content[i].Content[j]), "\n", " "))
			}
			rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
		}
		return strings.Join(rows, "\n")
	case "rule":
		return "---"
	case "mediaSingle", "mediaGroup":
		return "[attachment]"
	default:
		if len(n.Content) > 0 {
			return adfToText(n)
		}
		return renderInline([]adfNode{*n})
	}
}

func renderListItem(n *adfNode) string {
	// A list item is a sequence of blocks; join with a newline and indent.
	var parts []string
	for i := range n.Content {
		parts = append(parts, renderBlock(&n.Content[i]))
	}
	return strings.ReplaceAll(strings.Join(parts, "\n"), "\n", "\n  ")
}

func renderInline(nodes []adfNode) string {
	var sb strings.Builder
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case "text":
			sb.WriteString(n.Text)
		case "hardBreak":
			sb.WriteString("\n")
		case "mention":
			if t, ok := n.Attrs["text"].(string); ok {
				sb.WriteString(t)
			} else {
				sb.WriteString("@unknown")
			}
		case "inlineCard", "blockCard", "embedCard":
			if u, ok := n.Attrs["url"].(string); ok {
				sb.WriteString(u)
			}
		case "emoji":
			if s, ok := n.Attrs["shortName"].(string); ok {
				sb.WriteString(s)
			}
		case "date":
			if ts, ok := n.Attrs["timestamp"].(string); ok {
				sb.WriteString(ts)
			}
		default:
			if len(n.Content) > 0 {
				sb.WriteString(renderInline(n.Content))
			} else if n.Text != "" {
				sb.WriteString(n.Text)
			} else {
				sb.WriteString(fmt.Sprintf("[%s]", n.Type))
			}
		}
	}
	return sb.String()
}

// textToADF wraps plain text in a minimal ADF document, one paragraph per line.
func textToADF(s string) adfNode {
	doc := adfNode{Type: "doc", Version: 1}
	for _, line := range strings.Split(s, "\n") {
		p := adfNode{Type: "paragraph"}
		if line != "" {
			p.Content = []adfNode{{Type: "text", Text: line}}
		}
		doc.Content = append(doc.Content, p)
	}
	return doc
}
