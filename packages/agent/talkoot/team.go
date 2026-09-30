package talkoot

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/look"
)

// SetColor returns the roster text with the team colour set to color, or with
// no colour when color is empty, so the talkoot id picks one. It edits only
// that key, so the rest of the text keeps its comments and order.
func SetColor(text []byte, color string) ([]byte, error) {
	if color != "" && !look.ValidColor(color) {
		return nil, fmt.Errorf("talkoot: color %q is not a #RRGGBB value", color)
	}
	before, err := Parse(text, FileName)
	if err != nil {
		return nil, err
	}
	front, body, ok := splitFrontmatter(text)
	if !ok {
		return nil, errors.New("talkoot: the roster has no YAML frontmatter")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("talkoot: the roster frontmatter is not a mapping")
	}
	if color == "" {
		dropField(doc.Content[0], "color")
	} else if err := setField(doc.Content[0], "color", color); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	out := append([]byte("---\n"), buf.Bytes()...)
	out = append(out, "---\n"...)
	out = append(out, body...)
	after, err := Parse(out, FileName)
	if err != nil {
		return nil, err
	}
	// The edit must change the colour and nothing else.
	before.Color = color
	if !reflect.DeepEqual(before, after) {
		return nil, errors.New("talkoot: the roster text did not take the colour as set")
	}
	return out, nil
}
