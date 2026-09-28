// Package yamlfile is the one path by which HiveDispatch rewrites a YAML
// file on the operator's behalf: stage the new content (it must parse, the
// file's own loader reports what is still wrong, and the operator sees a
// diff), then commit it, keeping the previous version as .bak.
package yamlfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Staged is a proposed new content for a file, not yet written.
type Staged struct {
	Path     string
	Old      []byte // nil when the file does not exist yet
	New      []byte
	Diff     string
	Problems string // what the file's loader still objects to; empty when clean
}

// Stage checks content as the new contents of path. Content that does not
// parse as YAML is an error. validate, when set, is given a temporary file
// beside path holding content (so relative lookups behave as they will
// after the write); its error becomes Problems, with the temporary path
// replaced by the real one. Nothing at path is changed.
func Stage(path string, content []byte, validate func(tmp string) error) (Staged, error) {
	if len(bytes.TrimSpace(content)) == 0 {
		return Staged{}, errors.New("content is empty")
	}
	var probe any
	if err := yaml.Unmarshal(content, &probe); err != nil {
		return Staged{}, fmt.Errorf("not valid YAML, nothing written: %w", err)
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Staged{}, err
	}
	s := Staged{Path: path, Old: old, New: content, Diff: Diff(filepath.Base(path), string(old), string(content))}
	if validate == nil {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Staged{}, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return Staged{}, err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := validate(tmp); err != nil {
		s.Problems = strings.ReplaceAll(err.Error(), tmp, path)
	}
	return s, nil
}

// Commit writes the staged content: the previous file, if any, is kept as
// path.bak, and the new one is renamed into place.
func (s Staged) Commit() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	if s.Old != nil {
		if err := os.WriteFile(s.Path+".bak", s.Old, 0o600); err != nil {
			return err
		}
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, s.New, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Patch sets keys in a YAML mapping document and returns the result.
// Keys are dotted paths ("claude.model"); missing mappings along the path
// are created, and a nil value deletes the key. The document is edited as
// a node tree, so comments and key order survive.
func Patch(raw []byte, set map[string]any) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("not valid YAML: %w", err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("the document is not a mapping")
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := setPath(root, strings.Split(k, "."), set[k]); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	return out.Bytes(), nil
}

// setPath sets (or, for nil, deletes) path under the mapping m.
func setPath(m *yaml.Node, path []string, v any) error {
	i := lookup(m, path[0])
	if len(path) > 1 {
		if i < 0 {
			if v == nil {
				return nil
			}
			m.Content = append(m.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]},
				&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
			i = len(m.Content) - 2
		}
		child := m.Content[i+1]
		if child.Kind == yaml.ScalarNode && child.Tag == "!!null" {
			*child = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", LineComment: child.LineComment}
		}
		if child.Kind != yaml.MappingNode {
			return fmt.Errorf("%s is not a mapping", path[0])
		}
		return setPath(child, path[1:], v)
	}
	if v == nil {
		if i >= 0 {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
		}
		return nil
	}
	var val yaml.Node
	if err := val.Encode(v); err != nil {
		return err
	}
	if i < 0 {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}, &val)
		return nil
	}
	old := m.Content[i+1]
	val.HeadComment, val.LineComment, val.FootComment = old.HeadComment, old.LineComment, old.FootComment
	*old = val
	return nil
}

// lookup returns the index of key's key node in mapping m, or -1.
func lookup(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}
