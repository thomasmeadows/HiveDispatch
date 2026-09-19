package jira

import (
	"encoding/json"
	"testing"
)

func TestADFToTextRendersCommonNodes(t *testing.T) {
	raw := `{
	  "type":"doc","version":1,"content":[
	    {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]},
	    {"type":"paragraph","content":[
	      {"type":"text","text":"Hello "},
	      {"type":"mention","attrs":{"id":"abc","text":"@Thomas"}},
	      {"type":"text","text":", see "},
	      {"type":"inlineCard","attrs":{"url":"https://example.com/x"}},
	      {"type":"hardBreak"},
	      {"type":"text","text":"second line"}
	    ]},
	    {"type":"bulletList","content":[
	      {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
	      {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}
	    ]},
	    {"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(1)"}]}
	  ]}`
	var n adfNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	got := adfToText(&n)
	want := "Title\n\nHello @Thomas, see https://example.com/x\nsecond line\n\n- one\n- two\n\n```go\nfmt.Println(1)\n```"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestADFToTextNilAndEmpty(t *testing.T) {
	if adfToText(nil) != "" {
		t.Error("nil should render empty")
	}
	if adfToText(&adfNode{Type: "doc"}) != "" {
		t.Error("empty doc should render empty")
	}
}

func TestTextToADFRoundTrip(t *testing.T) {
	n := textToADF("line one\n\nline three")
	if n.Type != "doc" || len(n.Content) != 3 {
		t.Fatalf("doc = %+v", n)
	}
	if n.Content[0].Type != "paragraph" || n.Content[0].Content[0].Text != "line one" {
		t.Errorf("para 0 = %+v", n.Content[0])
	}
	if len(n.Content[1].Content) != 0 {
		t.Errorf("empty line should be empty paragraph, got %+v", n.Content[1])
	}
	if adfToText(&n) != "line one\n\nline three" {
		t.Errorf("round trip = %q", adfToText(&n))
	}
}
