package talkoot

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extroots"
)

// A template is a roster with the parts that belong to one person and one
// checkout left out: it carries no home, and its budget is a suggestion.
// Creating a talkoot from one fills those in (Instantiate).
//
// 🔑 A template grants nothing on its own. The roster it becomes is the
// authority document, and it reaches $TERVA_HOME only through talkoot.create,
// after a preview that shows every driver, posture, and budget. That is why a
// repository may ship a template and may not ship a roster (decision 0022
// rule 3).

// Template sources, in the order they shadow each other. A repository
// template sits in its own namespace instead, so it never shadows the others.
const (
	SourceUser    = "user"
	SourceBuiltin = "builtin"
	SourceRepo    = "repo"
	// SourceExtPrefix starts an extension source, "ext:<extension>".
	SourceExtPrefix = "ext:"
	// RepoPrefix starts the name of a repository template.
	RepoPrefix = "repo:"
)

// TemplatesDirName is the directory a user template or an extension's
// template lives in.
const TemplatesDirName = "talkoot-templates"

// maxRepoTemplates caps how many files the repository tier reads. A checkout
// is not trusted to be small, and the tier is read on every listing.
const maxRepoTemplates = 64

// maxRepoEntries caps how many directory entries the repository tier lists
// before it sorts them. Names are cheap, so it is far above the template cap.
const maxRepoEntries = 4096

// maxTemplateSize caps a template read from disk. A roster is a page of YAML,
// and a repository file is not trusted to be small.
const maxTemplateSize = 64 << 10

//go:embed templates/*.md
var builtinTemplates embed.FS

// Template is one template a talkoot can be created from.
type Template struct {
	// Name is the file stem, and "repo:<stem>" for a repository template.
	Name        string
	Title       string
	Description string
	// Source is user, ext:<extension>, builtin, or repo.
	Source string
	// Path is where the template was read, for messages.
	Path    string
	Members int
	Raw     []byte
	// Problem says why the template cannot be used. A template with a problem
	// is still listed, so a person sees it and can fix it.
	Problem string
}

// TemplatesDir is the user tier: $TERVA_HOME/talkoot-templates.
func TemplatesDir() string { return filepath.Join(config.TervaHome(), TemplatesDirName) }

// RepoTemplatesDir is where a checkout ships its templates.
func RepoTemplatesDir(checkout string) string {
	return filepath.Join(checkout, ".terva", "talkoot")
}

// Templates lists every template: the user tier, then enabled global
// extensions, then the built-in ones, each shadowing a lower tier's template of
// the same name, and last the templates of the checkout at repoDir. An empty
// repoDir skips the repository tier.
func Templates(repoDir string) []Template {
	seen := map[string]bool{}
	var out []Template
	add := func(list []Template) {
		for _, t := range list {
			// The repo: namespace belongs to the repository tier. A user or
			// extension file named repo:x.md would otherwise take the name
			// first and hide the checkout's template.
			if t.Source != SourceRepo && strings.HasPrefix(t.Name, RepoPrefix) {
				continue
			}
			if seen[t.Name] {
				continue
			}
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	add(readTemplateDir(TemplatesDir(), SourceUser, ""))
	add(extensionTemplates())
	add(builtinTemplateList())
	if repoDir != "" {
		add(repoTemplates(repoDir))
	}
	return out
}

// LookupTemplate finds one template by name.
func LookupTemplate(repoDir, name string) (Template, bool) {
	if !strings.HasPrefix(name, RepoPrefix) {
		repoDir = ""
	}
	for _, t := range Templates(repoDir) {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

// extensionTemplates reads the talkoot-templates directory of each enabled
// global extension. Like personas, it reads global roots only and the user
// layer's disable_extensions only: this package has no cwd and no trust
// verdict (see persona.listExtensionPersonas).
func extensionTemplates() []Template {
	disabled := []string{}
	if cfg, err := config.LoadConfig(); err == nil {
		disabled = cfg.DisableExtensions
	}
	var out []Template
	for _, r := range extroots.Enabled(config.TervaHome(), "", extroots.Gate{Disabled: disabled}) {
		dir, ok := r.SubDir(TemplatesDirName)
		if !ok {
			continue
		}
		out = append(out, readTemplateDir(dir, SourceExtPrefix+r.Name(), "")...)
	}
	return out
}

func builtinTemplateList() []Template {
	entries, err := fs.ReadDir(builtinTemplates, "templates")
	if err != nil {
		return nil
	}
	var out []Template
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok {
			continue
		}
		raw, err := builtinTemplates.ReadFile("templates/" + e.Name())
		if err != nil {
			continue
		}
		out = append(out, parseTemplate(stem, raw, SourceBuiltin, "embedded:templates/"+e.Name()))
	}
	return out
}

// readTemplateDir reads every *.md file in a directory of the user's own, the
// user tier or an extension's. A symlink there is the user's choice, so it is
// followed. A file that cannot be read is listed with its problem rather than
// hidden.
func readTemplateDir(dir, source, prefix string) []Template {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Template
	for _, e := range entries {
		name, ok := templateName(e, prefix)
		if !ok {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if bad := badStem(name, prefix); bad != "" {
			out = append(out, Template{Name: name, Source: source, Path: path, Problem: bad})
			continue
		}
		raw, err := readCapped(func() (*os.File, error) { return os.Open(path) })
		if err != nil {
			out = append(out, Template{Name: name, Source: source, Path: path, Problem: err.Error()})
			continue
		}
		out = append(out, parseTemplate(name, raw, source, path))
	}
	return out
}

// repoTemplates reads the templates a checkout ships in .terva/talkoot.
//
// 🚨 A cloned repository controls these files, and a preview shows their
// text. So every read goes through an os.Root at the checkout, which no
// symlink can lead out of, even when the tree changes during the read. A
// symlinked directory that stays inside the checkout is followed. A template
// file itself must be a regular file, never a symlink.
func repoTemplates(checkout string) []Template {
	root, entries := repoEntries(checkout)
	if root == nil {
		return nil
	}
	defer root.Close()
	var out []Template
	for _, e := range entries {
		name, _ := templateName(e, RepoPrefix)
		file := filepath.Join(repoRel, e.Name())
		path := filepath.Join(checkout, file)
		if bad := badStem(name, RepoPrefix); bad != "" {
			out = append(out, Template{Name: name, Source: SourceRepo, Path: path, Problem: bad})
			continue
		}
		raw, err := readRegular(root, file)
		if err != nil {
			out = append(out, Template{Name: name, Source: SourceRepo, Path: path, Problem: err.Error()})
			continue
		}
		out = append(out, parseTemplate(name, raw, SourceRepo, path))
	}
	return out
}

// repoRel is the repository tier's directory inside a checkout.
var repoRel = filepath.Join(".terva", "talkoot")

// RepoTemplateNames lists the names of a checkout's templates and reads none
// of them. A workspace that does not trust the checkout lists these, so a
// person sees what the repository ships without the daemon parsing it.
func RepoTemplateNames(checkout string) []string {
	root, entries := repoEntries(checkout)
	if root == nil {
		return nil
	}
	root.Close()
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name, _ := templateName(e, RepoPrefix)
		out = append(out, name)
	}
	return out
}

// repoEntries opens an os.Root at the checkout and returns the template
// entries of its .terva/talkoot, at most maxRepoTemplates of them. The caller
// closes the root. A nil root means the checkout ships no templates.
func repoEntries(checkout string) (*os.Root, []os.DirEntry) {
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return nil, nil
	}
	dir, err := root.Open(repoRel)
	if err != nil {
		root.Close()
		return nil, nil
	}
	// ReadDir(n) stops after n entries, so a huge directory costs one bounded
	// read of names. The names are sorted before the cap applies, so the
	// same checkout keeps the same templates whatever order the file system
	// lists them in, as long as the directory holds at most maxRepoEntries.
	all, _ := dir.ReadDir(maxRepoEntries)
	dir.Close()
	var out []os.DirEntry
	for _, e := range all {
		if _, ok := templateName(e, RepoPrefix); ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	if len(out) > maxRepoTemplates {
		out = out[:maxRepoTemplates]
	}
	return root, out
}

// templateName is the template name a directory entry gives, and false for an
// entry that is not a template: a directory, a file that is not *.md, or a
// README.
func templateName(e os.DirEntry, prefix string) (string, bool) {
	stem, ok := strings.CutSuffix(e.Name(), ".md")
	if !ok || e.IsDir() || strings.EqualFold(stem, "readme") {
		return "", false
	}
	return prefix + stem, true
}

func badStem(name, prefix string) string {
	stem := strings.TrimPrefix(name, prefix)
	if ValidID(stem) {
		return ""
	}
	return fmt.Sprintf("the file name %q must be lower case letters, digits, and dashes, starting with a letter", stem)
}

// readRegular reads a regular file inside root. The file it opens must be the
// file it checked, so a swap between the check and the open is refused.
func readRegular(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file; a repository template cannot be a symlink")
	}
	return readCapped(func() (*os.File, error) {
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		if got, err := f.Stat(); err != nil || !os.SameFile(info, got) {
			f.Close()
			return nil, errors.New("the file changed while it was read")
		}
		return f, nil
	})
}

// readCapped reads at most maxTemplateSize bytes from the file open returns.
func readCapped(open func() (*os.File, error)) ([]byte, error) {
	f, err := open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxTemplateSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxTemplateSize {
		return nil, fmt.Errorf("larger than %d KiB", maxTemplateSize>>10)
	}
	return raw, nil
}

// parseTemplate reads a template's frontmatter. Problems land on the
// Template rather than in an error, so the listing still shows the file.
func parseTemplate(name string, raw []byte, source, path string) Template {
	t := Template{Name: name, Source: source, Path: path, Raw: raw}
	doc, _, err := templateDoc(raw)
	if err != nil {
		t.Problem = err.Error()
		return t
	}
	if v := mapValue(doc, "description"); v != nil {
		t.Description = strings.TrimSpace(v.Value)
	}
	// Parse the roster without the description, the one key a roster lacks,
	// so an unknown key or a mistyped field is refused the way a roster's is.
	deleteKey(doc, "description")
	front, err := encodeFront(doc)
	if err != nil {
		t.Problem = err.Error()
		return t
	}
	r, err := Parse(append(append([]byte("---\n"), front...), "---\n"...), path)
	if err != nil {
		t.Problem = err.Error()
		return t
	}
	t.Title = r.Title
	if t.Title == "" {
		t.Title = r.Name
	}
	t.Members = len(r.Members)
	return t
}

// templateDoc splits a template into its frontmatter mapping and its body,
// and refuses a template that sets home.
func templateDoc(raw []byte) (*yaml.Node, []byte, error) {
	front, body, ok := splitFrontmatter(raw)
	if !ok {
		return nil, nil, errors.New("no YAML frontmatter between --- lines")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("the frontmatter is not a mapping")
	}
	m := doc.Content[0]
	if mapValue(m, "home") != nil {
		return nil, nil, errors.New("a template sets no home; the person picks it when creating the talkoot")
	}
	return m, body, nil
}

// Instantiate writes the roster a template becomes: home set, the budget
// replaced when budget is above zero, the description removed, and the members
// in drop removed. It does not validate the roster. The caller does, against
// its own environment.
func Instantiate(t Template, home string, budget float64, drop []string) ([]byte, error) {
	if t.Problem != "" {
		return nil, fmt.Errorf("talkoot: template %s: %s", t.Name, t.Problem)
	}
	if strings.TrimSpace(home) == "" {
		return nil, errors.New("talkoot: name the home checkout the talkoot runs in")
	}
	if !finite(budget) || budget < 0 {
		return nil, errors.New("talkoot: budget_usd_per_day is not a positive number")
	}
	doc, body, err := templateDoc(t.Raw)
	if err != nil {
		return nil, fmt.Errorf("talkoot: template %s: %w", t.Name, err)
	}
	deleteKey(doc, "description")
	// 🔑 Tagged as a string, so a home such as ~ or null is quoted and stays
	// a path rather than becoming null.
	setAfter(doc, "home", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: home}, "title", "name")
	if budget > 0 {
		setAfter(doc, "budget_usd_per_day", scalar(strconv.FormatFloat(budget, 'f', -1, 64)), "home")
	}
	if err := dropMembers(doc, drop); err != nil {
		return nil, fmt.Errorf("talkoot: template %s: %w", t.Name, err)
	}
	front, err := encodeFront(doc)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(front)
	out.WriteString("---\n")
	if charter := strings.TrimSpace(strings.ReplaceAll(string(body), "\r\n", "\n")); charter != "" {
		out.WriteString("\n" + charter + "\n")
	}
	return out.Bytes(), nil
}

func dropMembers(doc *yaml.Node, drop []string) error {
	if len(drop) == 0 {
		return nil
	}
	members := mapValue(doc, "members")
	if members == nil || members.Kind != yaml.SequenceNode {
		return errors.New("members is not a list")
	}
	found := map[string]bool{}
	kept := members.Content[:0]
	for _, m := range members.Content {
		id := ""
		if v := mapValue(m, "id"); v != nil {
			id = v.Value
		}
		if slices.Contains(drop, id) {
			found[id] = true
			continue
		}
		kept = append(kept, m)
	}
	members.Content = kept
	for _, id := range drop {
		if !found[id] {
			return fmt.Errorf("cannot drop %q: the template has no member with that id", id)
		}
	}
	return nil
}

func encodeFront(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

// mapValue returns the value under key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// setAfter sets key to v, replacing an existing value in place. A new key goes
// after the first of the named keys the mapping has, or first.
func setAfter(m *yaml.Node, key string, v *yaml.Node, after ...string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	at := 0
	for _, a := range after {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == a {
				at = i + 2
				break
			}
		}
		if at > 0 {
			break
		}
	}
	pair := []*yaml.Node{scalar(key), v}
	m.Content = append(m.Content[:at], append(pair, m.Content[at:]...)...)
}
