package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/i18n"

	"terva.sh/terva/packages/privfs"
)

// runLocaleCommand handles `terva locale ...`: inspect, scaffold, validate,
// and contribute UI translations. Mirrors the other subcommand routers.
func runLocaleCommand(rawArgs []string) (bool, error) {
	if len(rawArgs) == 0 || rawArgs[0] != "locale" {
		return false, nil
	}
	if len(rawArgs) == 1 {
		return true, localeList()
	}
	switch rawArgs[1] {
	case "list", "ls":
		return true, localeList()
	case "init":
		return true, localeInit(rawArgs[2:])
	case "validate", "check":
		return true, localeValidate(rawArgs[2:])
	case "diff", "status":
		return true, localeDiff(rawArgs[2:])
	case "merge":
		return true, localeMerge(rawArgs[2:])
	case "export":
		return true, localeExport(rawArgs[2:])
	case "help", "-h", "--help":
		printLocaleHelp()
		return true, nil
	default:
		printLocaleHelp()
		return true, i18n.Errorf("unknown locale subcommand: %s", rawArgs[1])
	}
}

func printLocaleHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.locale", `terva locale — inspect, edit, and contribute UI translations

usage:
  terva locale list                 show languages and translation coverage
  terva locale init <lang>          scaffold $TERVA_HOME/locales/<lang>.json (UI)
                                    + web/ + prompts/ + help/ <lang>.json to edit
  terva locale validate <file>...   check JSON + that %-args match the source
  terva locale diff <lang>          show missing / changed strings vs the source
  terva locale merge <lang>         fold a filled-in <lang>.todo.json into <lang>.json
  terva locale export <lang>        write a clean file ready to PR into the project

Catalogs: short UI strings (<lang>.json, English-as-key) and the web control
panel (web/<lang>.json, same English-as-key shape, served to the browser); plus
two dotted-key catalogs of large stable text — model-facing prompts
(prompts/<lang>.json: the /study task, auto-swarm summary, base system-prompt
segments, compaction) and operator-facing help (help/<lang>.json: the big
"terva <cmd> --help" screens). Each dotted-key file is seeded with the English
to rewrite; leaving one English keeps the default. Translating the prompts lets
an operator whose model answers in their language keep the whole exchange
coherent.

Choose a language with the TERVA_LANG env var or the "language" field in
config.json (a BCP-47 tag such as fi or pt-BR); English is the built-in
source. Set TERVA_CAPTURE_LOCALE=1 while using terva to record every
untranslated string into $TERVA_HOME/locales/<lang>.todo.json (UI) and the
<catalog>/<lang>.todo.json files as you go, then fill them in and run
"terva locale merge". See the write-terva-locale skill for the full authoring +
contribution guide.`))
}

// localeList prints coverage for every known target language (embedded ∪
// on-disk), marking the active one.
func localeList() error {
	ref, err := i18n.ReferenceDoc()
	if err != nil {
		return err
	}
	// The dotted-key catalogs (prompts, help), each counted separately.
	keyedRefs := map[string]map[string]string{}
	for _, cat := range i18n.KeyedCatalogs() {
		kr, err := i18n.KeyedReferenceDoc(cat)
		if err != nil {
			return err
		}
		keyedRefs[cat] = kr
	}
	// The non-root English-as-key catalogs (web), counted separately too.
	uiRefs := map[string]i18n.Doc{}
	for _, cat := range i18n.UICatalogs() {
		r, err := i18n.ReferenceDocIn(cat)
		if err != nil {
			return err
		}
		uiRefs[cat] = r
	}
	total := len(ref.Singular) + len(ref.Plural)
	fmt.Printf("source: en — %d translatable string(s)", total)
	for _, cat := range i18n.KeyedCatalogs() {
		if n := len(keyedRefs[cat]); n > 0 {
			fmt.Printf(", %d %s", n, cat)
		}
	}
	for _, cat := range i18n.UICatalogs() {
		if n := len(uiRefs[cat].Singular) + len(uiRefs[cat].Plural); n > 0 {
			fmt.Printf(", %d %s", n, cat)
		}
	}
	fmt.Printf("\nactive: %s\n\n", i18n.ActiveLang())

	langs := map[string]bool{}
	for _, n := range i18n.EmbeddedLocaleNames() {
		langs[n] = true
	}
	if entries, err := os.ReadDir(config.LocalesDir()); err == nil {
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".json")
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || name == "en" ||
				strings.HasSuffix(name, ".todo") || strings.HasSuffix(name, ".export") {
				continue
			}
			langs[name] = true
		}
	}
	if len(langs) == 0 {
		fmt.Println("no target languages yet — start one with `terva locale init <lang>`")
		return nil
	}
	for _, name := range sortedKeys(langs) {
		merged, err := i18n.LoadMerged(name, config.TervaHome())
		if err != nil {
			fmt.Printf("  %-8s (error: %v)\n", name, err)
			continue
		}
		done, tot := coverage(merged, ref)
		pct := 100
		if tot > 0 {
			pct = done * 100 / tot
		}
		mark := " "
		if name == i18n.ActiveLang() {
			mark = "*"
		}
		fmt.Printf("%s %-8s %3d%%  (%d/%d)  %s", mark, name, pct, done, tot, localeSource(name))
		for _, cat := range i18n.KeyedCatalogs() {
			kr := keyedRefs[cat]
			if len(kr) == 0 {
				continue
			}
			if mk, err := i18n.LoadMergedKeyed(cat, name, config.TervaHome()); err == nil {
				fmt.Printf("  [%s %d/%d]", cat, keyedDone(mk, kr), len(kr))
			}
		}
		for _, cat := range i18n.UICatalogs() {
			r := uiRefs[cat]
			if len(r.Singular)+len(r.Plural) == 0 {
				continue
			}
			if m, err := i18n.LoadMergedIn(cat, name, config.TervaHome()); err == nil {
				done, tot := coverage(m, r)
				fmt.Printf("  [%s %d/%d]", cat, done, tot)
			}
		}
		fmt.Println()
	}
	return nil
}

// localeSource labels where a language's strings come from.
func localeSource(name string) string {
	embedded := slices.Contains(i18n.EmbeddedLocaleNames(), name)
	_, diskErr := os.Stat(filepath.Join(config.LocalesDir(), name+".json"))
	onDisk := diskErr == nil
	switch {
	case embedded && onDisk:
		return "embedded + overlay"
	case embedded:
		return "embedded"
	default:
		return "on-disk"
	}
}

// localeInit scaffolds $TERVA_HOME/locales/<lang>.json with every source
// string (English keys → empty values to fill in), preserving anything
// already translated. Idempotent, like `terva persona init`.
func localeInit(args []string) error {
	lang := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return i18n.Errorf("unknown flag %q; usage: terva locale init <lang>", a)
		}
		if lang != "" {
			return i18n.Errorf("usage: terva locale init <lang>")
		}
		lang = a
	}
	if lang == "" {
		return i18n.Errorf("usage: terva locale init <lang>   (e.g. terva locale init fi)")
	}
	// `init en` is the CUSTOMIZE-in-place case (not translation): it
	// scaffolds an English overlay seeded with the current English so an
	// operator can reword UI strings or override prompts in place, keeping
	// what they don't touch. Other "en*" tags don't map to a loaded overlay
	// (the English branch always loads en.json), so steer them to `en`.
	english := lang == "en"
	if !english && strings.HasPrefix(strings.ToLower(lang), "en") {
		return i18n.Errorf("to customize English in place run `terva locale init en`; to translate pick a target like fi or pt-BR")
	}
	ref, err := i18n.ReferenceDoc()
	if err != nil {
		return err
	}
	// Seed from the translations that already resolve (embedded + any
	// overlay) so tweaking an existing language starts from its current
	// wording — like `terva persona init` copies the real crew to edit.
	// A brand-new language seeds blanks (which fall through to English at
	// runtime until filled).
	merged, err := i18n.LoadMerged(lang, config.TervaHome())
	if err != nil {
		return err
	}
	path := filepath.Join(config.LocalesDir(), lang+".json")
	out := loadRawLocale(path)
	forms := i18n.FormsForLang(lang)
	added := 0
	for k := range ref.Singular {
		if _, ok := out[k]; ok {
			continue
		}
		out[k] = i18n.MarshalValue(merged.Singular[k])
		added++
	}
	for k := range ref.Plural {
		if _, ok := out[k]; ok {
			continue
		}
		scaffold := map[string]string{}
		for _, f := range forms {
			scaffold[f] = ""
		}
		maps.Copy(scaffold, merged.Plural[k])
		out[k] = i18n.MarshalValue(scaffold)
		added++
	}
	// The ROOT catalog being complete says nothing about the keyed catalogs or
	// the web panel's, which the two calls below scaffold. Returning here
	// skipped both, so once the root had settled `terva locale init <lang>`
	// printed "nothing to add" and did exactly that — a translator could not
	// pick up a new prompt, tool or panel string without deleting their locale
	// file and starting over.
	if added == 0 {
		fmt.Printf("%s already lists all %d source string(s).\n", path, len(out))
	} else {
		blank := 0
		for _, v := range out {
			if !isFilled(v) {
				blank++
			}
		}
		if err := writeLocale(path, out); err != nil {
			return err
		}
		if english {
			fmt.Printf("wrote %s: %d string(s), seeded with the current English to edit in place\n", path, len(out))
			fmt.Printf("reword the values you want to change; delete the rest (they fall back to English)\n")
		} else {
			fmt.Printf("wrote %s: %d string(s), %d still to translate\n", path, len(out), blank)
			fmt.Printf("edit it (english keys → your translations), then run: terva locale validate %s\n", path)
		}
	}
	if err := localeInitKeyed(lang); err != nil {
		return err
	}
	return localeInitUICatalogs(lang)
}

// localeInitUICatalogs scaffolds every non-root English-as-key catalog (the
// web control-panel strings) into $TERVA_HOME/locales/<cat>/<lang>.json,
// singular+plural like the root UI catalog. Folded into `terva locale init` so a
// translator gets the web panel's strings in the same pass.
func localeInitUICatalogs(lang string) error {
	forms := i18n.FormsForLang(lang)
	for _, cat := range i18n.UICatalogs() {
		ref, err := i18n.ReferenceDocIn(cat)
		if err != nil {
			return err
		}
		if len(ref.Singular)+len(ref.Plural) == 0 {
			continue
		}
		merged, err := i18n.LoadMergedIn(cat, lang, config.TervaHome())
		if err != nil {
			return err
		}
		path := filepath.Join(config.LocalesDir(), cat, lang+".json")
		out := loadRawLocale(path)
		added := 0
		for k := range ref.Singular {
			if _, ok := out[k]; ok {
				continue
			}
			out[k] = i18n.MarshalValue(merged.Singular[k])
			added++
		}
		for k := range ref.Plural {
			if _, ok := out[k]; ok {
				continue
			}
			scaffold := map[string]string{}
			for _, f := range forms {
				scaffold[f] = ""
			}
			maps.Copy(scaffold, merged.Plural[k])
			out[k] = i18n.MarshalValue(scaffold)
			added++
		}
		if added == 0 {
			fmt.Printf("%s already lists all %d %s string(s); nothing to add.\n", path, len(out), cat)
			continue
		}
		blank := 0
		for _, v := range out {
			if !isFilled(v) {
				blank++
			}
		}
		if err := writeLocale(path, out); err != nil {
			return err
		}
		fmt.Printf("wrote %s: %d %s string(s), %d still to translate\n", path, len(out), cat, blank)
	}
	return nil
}

// localeInitKeyed scaffolds every dotted-key catalog (prompts, help) into
// $TERVA_HOME/locales/<catalog>/<lang>.json (dotted key → the English
// template to rewrite), pre-filled from any translations that already
// resolve. These live in their own files and are authored by a different
// hand than the UI strings, so they get their own materialize step (folded
// into `terva locale init`).
func localeInitKeyed(lang string) error {
	for _, cat := range i18n.KeyedCatalogs() {
		ref, err := i18n.KeyedReferenceDoc(cat)
		if err != nil {
			return err
		}
		if len(ref) == 0 {
			continue
		}
		merged, err := i18n.LoadMergedKeyed(cat, lang, config.TervaHome())
		if err != nil {
			return err
		}
		path := filepath.Join(config.LocalesDir(), cat, lang+".json")
		out := loadRawLocale(path)
		added := 0
		for k, english := range ref {
			if _, ok := out[k]; ok {
				continue
			}
			// Seed with an existing translation if present, else the English
			// template (a prompt/help author edits the source, not a blank key).
			seed := merged[k]
			if seed == "" {
				seed = english
			}
			out[k] = i18n.MarshalValue(seed)
			added++
		}
		if added == 0 {
			fmt.Printf("%s already lists all %d %s entr(y/ies); nothing to add.\n", path, len(out), cat)
			continue
		}
		if err := writeLocale(path, out); err != nil {
			return err
		}
		fmt.Printf("wrote %s: %d %s entr(y/ies) to translate\n", path, len(out), cat)
	}
	return nil
}

// localeValidate checks locale files for parseability, printf-arg parity
// with the source, and unknown keys. Defaults to the active language's
// on-disk overlay when no file is given.
func localeValidate(args []string) error {
	if len(args) == 0 {
		if i18n.ActiveLang() == "en" {
			return i18n.Errorf("usage: terva locale validate <file>...")
		}
		args = []string{filepath.Join(config.LocalesDir(), i18n.ActiveLang()+".json")}
	}
	// Reference key set per catalog subdir ("" root, "web" the control panel);
	// a web/<lang>.json validates against the web keys, not the root UI keys.
	refCache := map[string]map[string]bool{}
	refSet := func(subdir string) map[string]bool {
		if s, ok := refCache[subdir]; ok {
			return s
		}
		s := map[string]bool{}
		for _, k := range i18n.ReferenceKeysIn(subdir) {
			s[k] = true
		}
		refCache[subdir] = s
		return s
	}
	problems := 0
	for _, path := range args {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// A locales/<catalog>/<lang>.json is a flat dotted-key catalog with
		// its own reference; validate it against that catalog's source
		// (arg parity + unknown keys) rather than the UI keys.
		if cat := keyedCatalogOf(path); cat != "" {
			ref, err := i18n.KeyedReferenceDoc(cat)
			if err != nil {
				return err
			}
			problems += validateKeyedFile(path, data, ref)
			continue
		}
		refKeys := refSet(uiCatalogOf(path))
		doc, err := i18n.ParseLocaleFile(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			problems++
			continue
		}
		for k, v := range doc.Singular {
			if v == "" {
				continue
			}
			if !sameVerbs(i18n.Verbs(k), i18n.Verbs(v)) {
				fmt.Fprintf(os.Stderr, "%s: %q\n  source args %v, translation args %v\n",
					path, oneLine(k), i18n.Verbs(k), i18n.Verbs(v))
				problems++
			}
			if len(refKeys) > 0 && !refKeys[k] {
				fmt.Fprintf(os.Stderr, "%s: note: %q is not a current source string (english may have changed)\n", path, oneLine(k))
			}
		}
		for k, forms := range doc.Plural {
			_, other, _ := i18n.SplitPluralKey(k)
			want := i18n.Verbs(other)
			for cat, form := range forms {
				if form == "" {
					continue
				}
				if !sameVerbs(want, i18n.Verbs(form)) {
					fmt.Fprintf(os.Stderr, "%s: %q [%s]\n  source args %v, translation args %v\n",
						path, oneLine(k), cat, want, i18n.Verbs(form))
					problems++
				}
			}
		}
		fmt.Printf("%s: %d string(s), %d plural entr(y/ies)\n", path, len(doc.Singular), len(doc.Plural))
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	fmt.Println("ok")
	return nil
}

// localeDiff reports what a language is missing and which of its entries no
// longer match a source string (with a fuzzy "did you mean" for those).
func localeDiff(args []string) error {
	lang := i18n.ActiveLang()
	if len(args) > 0 {
		lang = args[0]
	}
	if lang == "" || lang == "en" {
		return i18n.Errorf("usage: terva locale diff <lang>")
	}
	if err := diffUICatalog("", lang); err != nil {
		return err
	}
	for _, cat := range i18n.UICatalogs() {
		fmt.Println()
		if err := diffUICatalog(cat, lang); err != nil {
			return err
		}
	}
	return nil
}

// diffUICatalog reports the missing / orphaned strings of one English-as-key
// catalog (subdir "" = root UI, "web" = control panel) for lang. A section is
// labelled by its catalog when non-root; an empty catalog prints nothing.
func diffUICatalog(subdir, lang string) error {
	ref, err := i18n.ReferenceDocIn(subdir)
	if err != nil {
		return err
	}
	if len(ref.Singular)+len(ref.Plural) == 0 {
		return nil
	}
	merged, err := i18n.LoadMergedIn(subdir, lang, config.TervaHome())
	if err != nil {
		return err
	}
	refKeys := i18n.ReferenceKeysIn(subdir)
	refSet := map[string]bool{}
	for _, k := range refKeys {
		refSet[k] = true
	}

	var missing []string
	for k := range ref.Singular {
		if merged.Singular[k] == "" {
			missing = append(missing, k)
		}
	}
	for k := range ref.Plural {
		if f, ok := merged.Plural[k]; !ok || f["other"] == "" {
			missing = append(missing, k)
		}
	}
	var orphaned []string
	for k := range merged.Singular {
		if !refSet[k] {
			orphaned = append(orphaned, k)
		}
	}
	for k := range merged.Plural {
		if !refSet[k] {
			orphaned = append(orphaned, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphaned)

	label := lang
	if subdir != "" {
		label = lang + " [" + subdir + "]"
	}
	total := len(ref.Singular) + len(ref.Plural)
	fmt.Printf("%s: %d/%d translated\n", label, total-len(missing), total)
	if len(missing) > 0 {
		fmt.Printf("missing (%d):\n", len(missing))
		for _, k := range missing {
			fmt.Printf("  %s\n", oneLine(k))
		}
	}
	if len(orphaned) > 0 {
		fmt.Printf("orphaned — english may have changed (%d):\n", len(orphaned))
		for _, k := range orphaned {
			fmt.Printf("  %s\n", oneLine(k))
			if best := nearest(k, refKeys); best != "" {
				fmt.Printf("    ↳ did you mean: %s\n", oneLine(best))
			}
		}
	}
	if len(missing) == 0 && len(orphaned) == 0 {
		fmt.Println("fully translated and in sync.")
	}
	return nil
}

// localeMerge folds the filled-in entries of <lang>.todo.json into
// <lang>.json, leaving still-blank entries behind in the todo file.
func localeMerge(args []string) error {
	lang := i18n.ActiveLang()
	if len(args) > 0 {
		lang = args[0]
	}
	if lang == "" || lang == "en" {
		return i18n.Errorf("usage: terva locale merge <lang>")
	}
	todoPath := filepath.Join(config.LocalesDir(), lang+".todo.json")
	todo := loadRawLocale(todoPath)
	if len(todo) == 0 {
		fmt.Printf("no %s to merge\n", todoPath)
		return nil
	}
	destPath := filepath.Join(config.LocalesDir(), lang+".json")
	dest := loadRawLocale(destPath)
	moved := 0
	remaining := map[string]json.RawMessage{}
	for k, v := range todo {
		if isFilled(v) {
			dest[k] = v
			moved++
		} else {
			remaining[k] = v
		}
	}
	if moved == 0 {
		fmt.Printf("nothing filled in yet — edit %s first\n", todoPath)
		return nil
	}
	if err := writeLocale(destPath, dest); err != nil {
		return err
	}
	if len(remaining) == 0 {
		_ = os.Remove(todoPath)
	} else if err := writeLocale(todoPath, remaining); err != nil {
		return err
	}
	fmt.Printf("merged %d filled string(s) into %s; %d still pending\n", moved, destPath, len(remaining))
	return nil
}

// localeExport writes a clean, sorted file containing only the language's
// non-empty translations, ready to copy into the repo's embedded locales.
func localeExport(args []string) error {
	lang := i18n.ActiveLang()
	if len(args) > 0 {
		lang = args[0]
	}
	if lang == "" || lang == "en" {
		return i18n.Errorf("usage: terva locale export <lang>")
	}
	rootDest, rootN, err := exportUICatalog("", lang)
	if err != nil {
		return err
	}
	if rootN == 0 {
		return fmt.Errorf("%s has no translations yet — run `terva locale init %s` and fill it in", lang, lang)
	}
	fmt.Printf("wrote %s (%d translated string(s))\n", rootDest, rootN)
	for _, cat := range i18n.UICatalogs() {
		dest, n, err := exportUICatalog(cat, lang)
		if err != nil {
			return err
		}
		if n > 0 {
			fmt.Printf("wrote %s (%d translated %s string(s))\n", dest, n, cat)
		}
	}
	fmt.Println("to contribute this language to terva:")
	fmt.Printf("  1. copy each .export.json into packages/i18n/locales/ (root → %s.json, web → web/%s.json)\n", lang, lang)
	fmt.Println("  2. run `just test-pkg ./packages/i18n` and `terva locale validate` on them")
	fmt.Println("  3. open a PR — the write-terva-locale skill has the full guide")
	return nil
}

// exportUICatalog writes a clean, sorted export of one English-as-key catalog's
// non-empty translations for lang, returning the path and the count. subdir ""
// is the root UI catalog (locales/<lang>.export.json); "web" nests under
// locales/web/. A catalog with nothing translated writes nothing (count 0).
func exportUICatalog(subdir, lang string) (string, int, error) {
	merged, err := i18n.LoadMergedIn(subdir, lang, config.TervaHome())
	if err != nil {
		return "", 0, err
	}
	out := map[string]json.RawMessage{}
	for k, v := range merged.Singular {
		if v == "" {
			continue
		}
		out[k] = i18n.MarshalValue(v)
	}
	for k, forms := range merged.Plural {
		clean := map[string]string{}
		for c, f := range forms {
			if f != "" {
				clean[c] = f
			}
		}
		if len(clean) == 0 {
			continue
		}
		out[k] = i18n.MarshalValue(clean)
	}
	dir := config.LocalesDir()
	if subdir != "" {
		dir = filepath.Join(dir, subdir)
	}
	dest := filepath.Join(dir, lang+".export.json")
	if len(out) == 0 {
		return dest, 0, nil
	}
	if err := writeLocale(dest, out); err != nil {
		return dest, 0, err
	}
	return dest, len(out), nil
}

// keyedCatalogOf returns the keyed catalog (prompts, help) a path belongs to
// by its parent directory (locales/<catalog>/<lang>.json), or "" for a UI
// locale file.
func keyedCatalogOf(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	for _, cat := range i18n.KeyedCatalogs() {
		if dir == cat {
			return cat
		}
	}
	return ""
}

// uiCatalogOf returns the non-root English-as-key catalog (web) a path belongs
// to by its parent directory (locales/<cat>/<lang>.json), or "" for the root
// UI locale file. Used to validate a web/<lang>.json against the web reference.
func uiCatalogOf(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	for _, cat := range i18n.UICatalogs() {
		if dir == cat {
			return cat
		}
	}
	return ""
}

// validateKeyedFile checks a flat keyed catalog: JSON validity, printf-arg
// parity with the English template, and unknown keys. Returns the problem
// count (0 = clean).
func validateKeyedFile(path string, data []byte, ref map[string]string) int {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return 1
	}
	problems := 0
	for k, v := range m {
		if v == "" {
			continue
		}
		english, known := ref[k]
		if !known {
			fmt.Fprintf(os.Stderr, "%s: note: %q is not a current key (english may have changed)\n", path, oneLine(k))
			continue
		}
		if !sameVerbs(i18n.Verbs(english), i18n.Verbs(v)) {
			fmt.Fprintf(os.Stderr, "%s: %q\n  source args %v, translation args %v\n",
				path, oneLine(k), i18n.Verbs(english), i18n.Verbs(v))
			problems++
		}
	}
	fmt.Printf("%s: %d prompt(s)\n", path, len(m))
	return problems
}

// --- helpers ---

func loadRawLocale(path string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func writeLocale(path string, m map[string]json.RawMessage) error {
	data, err := i18n.MarshalLocale(m)
	if err != nil {
		return err
	}
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return privfs.WriteFileMode(path, data, 0o644)
}

// coverage counts how many source strings a merged locale actually
// translates (singular non-empty, or plural "other" non-empty).
func coverage(merged, ref i18n.Doc) (done, total int) {
	for k := range ref.Singular {
		total++
		if merged.Singular[k] != "" {
			done++
		}
	}
	for k := range ref.Plural {
		total++
		if f, ok := merged.Plural[k]; ok && f["other"] != "" {
			done++
		}
	}
	return done, total
}

// keyedDone counts how many entries a merged keyed catalog actually
// translates. These files are seeded with the English template (not
// blank), so "translated" means present and DIFFERENT from the English
// default — a still-English entry is untranslated, not done.
func keyedDone(merged, ref map[string]string) int {
	done := 0
	for k, english := range ref {
		if v, ok := merged[k]; ok && v != "" && v != english {
			done++
		}
	}
	return done
}

func isFilled(v json.RawMessage) bool {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return false
	}
	switch v[0] {
	case '"':
		var s string
		_ = json.Unmarshal(v, &s)
		return s != ""
	case '{':
		var m map[string]string
		_ = json.Unmarshal(v, &m)
		for _, f := range m {
			if f != "" {
				return true
			}
		}
	}
	return false
}

func sameVerbs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// oneLine collapses a (possibly multi-line) key to a single truncated line
// for terminal listing.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	r := []rune(s)
	if len(r) > 72 {
		return string(r[:69]) + "..."
	}
	return s
}

// nearest returns the reference key most similar to k by word-token overlap,
// or "" when nothing clears a modest threshold — a hint for a reworded
// source string, not an authoritative match.
func nearest(k string, refs []string) string {
	kt := tokenize(k)
	best, bestScore := "", 0.45
	for _, r := range refs {
		if s := jaccard(kt, tokenize(r)); s > bestScore {
			best, bestScore = r, s
		}
	}
	return best
}

func tokenize(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		set[w] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
