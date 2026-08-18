package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/i18n"
)

// runProjectCommand handles `terva project ...` — per-project settings that edit
// the local .terva/config.json. Today: managing which GLOBAL extensions a
// project-scoped agent re-adopts. Returns handled=false when rawArgs isn't a
// project command, mirroring the other subcommand routers.
func runProjectCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "project" {
		return false, nil
	}
	sub := ""
	var rest []string
	if len(rawArgs) >= 2 {
		sub = rawArgs[1]
		rest = rawArgs[2:]
	}
	// Every subverb, before any of them reads an argument. The subverbs that
	// take one used to persist it, so this is a state-change guard rather than
	// a convenience.
	if hasHelpArg(rest) {
		printProjectHelp()
		return true, nil
	}
	switch sub {
	case "init":
		return true, runProjectInit(rest)
	case "status", "info":
		return true, runProjectStatus()
	case "ext", "extensions":
		return true, runProjectExt(rest)
	case "model":
		return true, runProjectScalar("model", rest)
	case "provider":
		return true, runProjectScalar("provider", rest)
	case "trust":
		return true, runProjectTrust(false)
	case "untrust":
		return true, runProjectTrust(true)
	case "", "help", "-h", "--help":
		printProjectHelp()
		return true, nil
	default:
		printProjectHelp()
		return true, i18n.Errorf("unknown 'terva project' subcommand %q (try: terva project help)", sub)
	}
}

// hasHelpArg reports whether args carry one of the help forms every other verb
// in this binary honors.
//
// `terva project`'s subverbs read their argument straight out of rest[0], and
// only the TOP level checked for help. So a help probe became the value:
// `terva project model --help` wrote "--help" into .terva/config.json as the
// project's model, and `terva project ext disable --help` added it to the
// project's disable list. A probe must never persist anything.
//
// The flag forms count anywhere, so `terva project ext adopt foo --help`
// explains itself instead of adopting foo. The bare word counts only in the
// leading verb position, because everywhere else it is a legitimate value —
// `terva project init --persona help` names a persona.
func hasHelpArg(args []string) bool {
	for i, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
		if a == "help" && i == 0 {
			return true
		}
	}
	return false
}

func printProjectHelp() {
	fmt.Fprint(os.Stderr, i18n.H("help.project", `terva project — per-project settings (edits ./.terva/config.json)

  terva project init [--persona NAME]   scaffold a self-contained (project-scoped) agent here
  terva project status                  show what this project will run (scope, trust, extensions, model)
  terva project trust / untrust         trust this directory (REQUIRED to run it scoped) / revoke

  terva project ext list                adopted + disabled extensions, and what's available globally
  terva project ext adopt <name>        re-admit a global extension into this scoped project
  terva project ext drop <name>         stop adopting a global extension here
  terva project ext disable <name>      never load <name> in this project
  terva project ext enable <name>       undo a project-level disable

  terva project model [ID]              show or set this project's model
  terva project provider [NAME]         show or set this project's provider

A scoped project keeps all data in .terva/home, loads only its own (+ adopted)
extensions, and inherits only your login + trust. Because it runs the project's
own config/extensions/hooks, you must "terva project trust" it before it will
run scoped. See docs/extensions.md#project-scoped-agents.
`))
}

func runProjectExt(args []string) error {
	action := ""
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "list", "":
		return projectExtList()
	case "adopt":
		if len(args) < 2 {
			return i18n.Errorf("usage: terva project ext adopt <name>")
		}
		return projectExtAdopt(args[1])
	case "drop":
		if len(args) < 2 {
			return i18n.Errorf("usage: terva project ext drop <name>")
		}
		return projectExtDrop(args[1])
	case "disable":
		if len(args) < 2 {
			return i18n.Errorf("usage: terva project ext disable <name>")
		}
		return projectExtSetDisabled(args[1], true)
	case "enable":
		if len(args) < 2 {
			return i18n.Errorf("usage: terva project ext enable <name>")
		}
		return projectExtSetDisabled(args[1], false)
	default:
		return i18n.Errorf("usage: terva project ext <list|adopt|drop|disable|enable> [name]")
	}
}

// projectExtSetDisabled adds or removes a name from the project's restrict-only
// disable_extensions list. Disabling needs no global install (you may forbid an
// extension that isn't present); enabling only removes the PROJECT's veto — it
// can't re-enable one the user disabled globally.
func projectExtSetDisabled(name string, disable bool) error {
	name = strings.TrimSpace(name)
	list, err := readProjectList("disable_extensions")
	if err != nil {
		return err
	}
	has := false
	out := list[:0]
	for _, n := range list {
		if n == name {
			has = true
			if disable {
				out = append(out, n) // keep
			}
			continue
		}
		out = append(out, n)
	}
	switch {
	case disable && has:
		fmt.Printf("%q is already disabled in this project\n", name)
		return nil
	case disable:
		out = append(out, name)
		sort.Strings(out)
	case !disable && !has:
		fmt.Printf("%q is not disabled in this project\n", name)
		return nil
	}
	if err := setProjectList("disable_extensions", out); err != nil {
		return err
	}
	if disable {
		fmt.Printf("disabled %q in this project\n", name)
	} else {
		fmt.Printf("re-enabled %q (removed this project's veto)\n", name)
	}
	return nil
}

// projectConfigFile is the .terva/config.json in the current directory (where a
// project declares itself; the CLI writes the nearest one at the cwd).
func projectConfigFile() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".terva", "config.json"), nil
}

func globalExtNames() []string { return listDirNames(GlobalExtensionsDir()) }

func projectExtAdopt(name string) error {
	name = strings.TrimSpace(name)
	// Validate the extension is actually installed globally — adoption can only
	// select from your upstream set, never invent new code.
	if _, err := os.Stat(filepath.Join(GlobalExtensionsDir(), name, "extension.json")); err != nil {
		return fmt.Errorf("no global extension %q installed (see `terva ext list`)", name)
	}
	adopted, err := readAdoptList()
	if err != nil {
		return err
	}
	for _, a := range adopted {
		if a == name {
			fmt.Printf("already adopting %q in this project\n", name)
			return nil
		}
	}
	adopted = append(adopted, name)
	sort.Strings(adopted)
	if err := writeAdoptList(adopted); err != nil {
		return err
	}
	fmt.Printf("adopted %q — it will load here when this project is scoped and trusted\n", name)
	return nil
}

func projectExtDrop(name string) error {
	name = strings.TrimSpace(name)
	adopted, err := readAdoptList()
	if err != nil {
		return err
	}
	out := adopted[:0]
	found := false
	for _, a := range adopted {
		if a == name {
			found = true
			continue
		}
		out = append(out, a)
	}
	if !found {
		fmt.Printf("%q is not adopted in this project\n", name)
		return nil
	}
	if err := writeAdoptList(out); err != nil {
		return err
	}
	fmt.Printf("dropped %q\n", name)
	return nil
}

func projectExtList() error {
	adopted, err := readAdoptList()
	if err != nil {
		return err
	}
	if len(adopted) == 0 {
		fmt.Println("adopted in this project: (none)")
	} else {
		fmt.Println("adopted in this project:")
		for _, a := range adopted {
			fmt.Printf("  • %s\n", a)
		}
	}
	if disabled, _ := readProjectList("disable_extensions"); len(disabled) > 0 {
		fmt.Println("disabled in this project:")
		for _, d := range disabled {
			fmt.Printf("  • %s\n", d)
		}
	}
	globals := globalExtNames()
	if len(globals) == 0 {
		fmt.Println("\navailable to adopt (global): (none installed)")
		return nil
	}
	adoptedSet := map[string]bool{}
	for _, a := range adopted {
		adoptedSet[a] = true
	}
	fmt.Println("\navailable to adopt (global):")
	for _, g := range globals {
		mark := " "
		if adoptedSet[g] {
			mark = "✓"
		}
		fmt.Printf("  %s %s\n", mark, g)
	}
	return nil
}

// readProjectConfigMap reads the cwd's .terva/config.json as a generic map so
// edits preserve every field (known or not). A missing file is an empty map.
func readProjectConfigMap() (map[string]json.RawMessage, string, error) {
	path, err := projectConfigFile()
	if err != nil {
		return nil, "", err
	}
	m := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, path, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, path, nil
}

func writeProjectConfigMap(m map[string]json.RawMessage, path string) error {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// setProjectField sets (or, for a nil value, deletes) one key in the project
// config, preserving all others.
func setProjectField(key string, value any) error {
	m, path, err := readProjectConfigMap()
	if err != nil {
		return err
	}
	if value == nil {
		delete(m, key)
	} else {
		enc, err := json.Marshal(value)
		if err != nil {
			return err
		}
		m[key] = enc
	}
	return writeProjectConfigMap(m, path)
}

// readProjectList reads a string-array field from the project config.
func readProjectList(key string) ([]string, error) {
	m, _, err := readProjectConfigMap()
	if err != nil {
		return nil, err
	}
	var list []string
	if v, ok := m[key]; ok {
		_ = json.Unmarshal(v, &list)
	}
	return list, nil
}

// setProjectList sets a string-array field, removing the key when empty so the
// file never keeps a dangling "[]".
func setProjectList(key string, list []string) error {
	if len(list) == 0 {
		return setProjectField(key, nil)
	}
	return setProjectField(key, list)
}

func readAdoptList() ([]string, error)   { return readProjectList("adopt_extensions") }
func writeAdoptList(list []string) error { return setProjectList("adopt_extensions", list) }

// runProjectInit scaffolds a self-contained, project-scoped agent in the cwd:
// the config flag, the dirs where project extensions/personas go, and the
// gitignored data home. Idempotent — it merges config and skips existing files.
func runProjectInit(args []string) error {
	persona := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--persona" && i+1 < len(args) {
			persona, i = args[i+1], i+1
		} else if v, ok := strings.CutPrefix(args[i], "--persona="); ok {
			persona = v
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := setProjectField("project_scoped", true); err != nil {
		return err
	}
	for _, d := range []string{
		filepath.Join(cwd, ".terva", "extensions"),
		filepath.Join(cwd, ".terva", "personas"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	dataHome := filepath.Join(cwd, ".terva", "home")
	if err := os.MkdirAll(dataHome, 0o700); err != nil {
		return err
	}
	if gi := filepath.Join(dataHome, ".gitignore"); !fileExists(gi) {
		_ = os.WriteFile(gi, []byte("# terva project-scoped data home — runtime state, do not commit\n*\n"), 0o644)
	}

	fmt.Println("initialized a project-scoped agent in ./.terva")
	fmt.Println("  • .terva/config.json   (project_scoped: true)")
	fmt.Println("  • .terva/extensions/   (drop project extensions here)")
	fmt.Println("  • .terva/personas/     (project personas — load by path: --persona ./.terva/personas/NAME.md)")
	fmt.Println("  • .terva/home/         (runtime data; gitignored)")

	if persona != "" {
		slug := personaSlug(persona)
		path := filepath.Join(cwd, ".terva", "personas", slug+".md")
		if !fileExists(path) {
			tmpl := fmt.Sprintf("---\nname: %s\n# immersive: true   # uncomment to let this charter OWN the identity\n---\n\nDescribe who %s is and how it should behave. This charter layers on terva's\nidentity unless you set immersive: true.\n", persona, persona)
			if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
				return err
			}
			fmt.Printf("  • .terva/personas/%s.md  (starter persona — edit it)\n", slug)
		}
		fmt.Printf("\nnext: edit the persona, then run:  terva --persona ./.terva/personas/%s.md\n", slug)
	}
	fmt.Println("\nthen `terva project trust` to allow this project's extensions, and run `terva`.")
	return nil
}

func personaSlug(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "-")
}

// runProjectScalar shows (no arg) or sets/clears a single string project field
// such as model or provider.
func runProjectScalar(key string, args []string) error {
	if len(args) == 0 {
		m, _, err := readProjectConfigMap()
		if err != nil {
			return err
		}
		var v string
		if raw, ok := m[key]; ok {
			_ = json.Unmarshal(raw, &v)
		}
		if v == "" {
			fmt.Printf("project %s: (not set — the default / --%s applies)\n", key, key)
		} else {
			fmt.Printf("project %s: %s\n", key, v)
		}
		return nil
	}
	val := strings.TrimSpace(args[0])
	if val == "" || val == "clear" || val == "-" {
		if err := setProjectField(key, nil); err != nil {
			return err
		}
		fmt.Printf("cleared project %s\n", key)
		return nil
	}
	if err := setProjectField(key, val); err != nil {
		return err
	}
	fmt.Printf("set project %s = %s  (applies when the project is trusted)\n", key, val)
	return nil
}

// runProjectTrust trusts (or untrusts) the cwd via the global trust store — the
// same store `terva trust` uses, so a scoped project's trust stays global.
func runProjectTrust(untrust bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if untrust {
		if err := config.UntrustPath(cwd); err != nil {
			return err
		}
		fmt.Printf("untrusted %s (its project content will no longer load)\n", config.CanonicalTrustPath(cwd))
		return nil
	}
	if err := config.TrustPath(cwd, false); err != nil {
		return err
	}
	fmt.Printf("trusted %s (its project extensions/skills/context will load)\n", config.CanonicalTrustPath(cwd))
	return nil
}

// runProjectStatus prints what this directory will actually run: scope, trust,
// data home, the extensions that will load, and any project model/provider.
func runProjectStatus() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	scoped := resolveProjectScoped(build.Args{CWD: cwd}, cwd)
	trusted := permissions.ResolveTrustState(cwd, false).IsTrusted()

	fmt.Printf("project: %s\n", cwd)
	fmt.Printf("  scoped:  %v\n", scoped)
	fmt.Printf("  trusted: %v\n", trusted)
	if scoped {
		fmt.Printf("  data home: %s\n", projectDataHome(cwd))
		fmt.Println("  inherits from global: login + trust only")
	}

	own := listDirNames(filepath.Join(cwd, ".terva", "extensions"))
	adopted, _ := readProjectList("adopt_extensions")
	disabled, _ := readProjectList("disable_extensions")

	fmt.Println("  extensions:")
	if len(own) == 0 && len(adopted) == 0 && len(disabled) == 0 {
		fmt.Println("    (none declared)")
	}
	for _, e := range own {
		fmt.Printf("    • %-20s project\n", e)
	}
	for _, a := range adopted {
		note := "adopted (global ✓)"
		if !fileExists(filepath.Join(GlobalExtensionsDir(), a, "extension.json")) {
			note = "adopted (✗ not installed globally)"
		}
		fmt.Printf("    • %-20s %s\n", a, note)
	}
	for _, d := range disabled {
		fmt.Printf("    • %-20s disabled\n", d)
	}
	if !scoped {
		fmt.Println("    + all your global extensions (not scoped)")
	}

	m, _, _ := readProjectConfigMap()
	var model, provider string
	if raw, ok := m["model"]; ok {
		_ = json.Unmarshal(raw, &model)
	}
	if raw, ok := m["provider"]; ok {
		_ = json.Unmarshal(raw, &provider)
	}
	if model != "" || provider != "" {
		fmt.Printf("  model/provider: %s / %s\n", orDefault(model), orDefault(provider))
	}

	if scoped && !trusted {
		fmt.Println("\n  ⚠ not trusted — this project's extensions will NOT load. run: terva project trust")
	}
	return nil
}

func listDirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func orDefault(s string) string {
	if s == "" {
		return "(default)"
	}
	return s
}
