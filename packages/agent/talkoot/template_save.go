package talkoot

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/privfs"
)

// ErrTemplateExists is returned when a user template of the name exists.
var ErrTemplateExists = errors.New("talkoot: a template of that name exists")

// ErrTemplateInvalid wraps each refusal of the caller's input: a bad name, a
// template that is too large, or one that does not parse. Any other error
// from SaveTemplate is a storage failure.
var ErrTemplateInvalid = errors.New("talkoot: the template cannot be saved")

// TemplateFromRoster turns the text of a talkoot.md into a template: home,
// the team colour, and a team budget waiver are removed, and description is
// set when it is not empty. The charter stays.
//
// 🔑 These three keys belong to one team. home names one checkout, the colour
// tells one team from another, and a waiver is a person's decision about one
// team's spend (decision 0027). A template carries none of them.
func TemplateFromRoster(text []byte, description string) ([]byte, error) {
	front, body, ok := splitFrontmatter(text)
	if !ok {
		return nil, errors.New("talkoot: the roster has no YAML frontmatter between --- lines")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("talkoot: the roster frontmatter is not a mapping")
	}
	m := doc.Content[0]
	for _, k := range []string{"home", "color", "team_budget_waived", "description"} {
		deleteKey(m, k)
	}
	if d := strings.TrimSpace(description); d != "" {
		setAfter(m, "description", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: d}, "title", "name")
	}
	enc, err := encodeFront(m)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(enc)
	out.WriteString("---\n")
	if charter := strings.TrimSpace(string(body)); charter != "" {
		out.WriteString("\n" + charter + "\n")
	}
	return out.Bytes(), nil
}

// SaveTemplate writes a template to the user tier, $TERVA_HOME/talkoot-templates,
// as <name>.md. It refuses a name that is not an id, a template that does not
// parse, and a name the user tier already holds. An id holds no colon, so a
// saved name never enters the repo: namespace.
//
// A user template shadows an extension or built-in template of the same name.
// That is the user tier's purpose, so it is allowed.
func SaveTemplate(name string, text []byte) (string, error) {
	if !ValidID(name) {
		return "", fmt.Errorf("%w: name %q must be lower case letters, digits, and dashes, starting with a letter", ErrTemplateInvalid, name)
	}
	if len(text) > maxTemplateSize {
		return "", fmt.Errorf("%w: it is larger than %d bytes", ErrTemplateInvalid, maxTemplateSize)
	}
	if t := parseTemplate(name, text, SourceUser, name+".md"); t.Problem != "" {
		return "", fmt.Errorf("%w: it does not parse: %s", ErrTemplateInvalid, t.Problem)
	}
	dir := TemplatesDir()
	if err := privfs.MkdirAll(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	// O_EXCL claims the name, so two saves cannot both write it.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privfs.FileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: %s", ErrTemplateExists, path)
		}
		return "", err
	}
	if _, err := f.Write(text); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
