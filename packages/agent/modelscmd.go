package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/provider"
)

// modelsScaffold is the starter $TERVA_HOME/models.json written by
// `terva models init`. It is valid JSON (the loader uses strict
// encoding/json, so no comments are possible) and demonstrates the
// thing the docs call out as non-obvious: the openai-compatible
// provider stores ONE endpoint per /login, but models.json can carry
// as many endpoints as you like — each model entry pins its own
// baseUrl, so the two entries below point at two different servers and
// both show up in /model at once.
//
// Placeholder ids/urls are deliberately obvious so an unedited file
// fails loudly against a real server rather than silently "working".
const modelsScaffold = `{
  "providers": {
    "openai-compatible": {
      "models": [
        {
          "id": "REPLACE-with-a-model-id-your-server-serves",
          "name": "My Local Model",
          "reasoning": false,
          "contextWindow": 32768,
          "maxTokens": 4096,
          "baseUrl": "http://localhost:1234/v1",
          "capabilities": { "image-input": false }
        },
        {
          "id": "REPLACE-with-a-second-model-id",
          "name": "Second Endpoint",
          "reasoning": false,
          "contextWindow": 131072,
          "maxTokens": 8192,
          "baseUrl": "http://localhost:8000/v1"
        }
      ]
    }
  }
}
`

// runModelsCommand dispatches `terva models ...`. Returns (handled=true,
// err) if rawArgs starts with "models"; otherwise (handled=false, nil)
// so the main router falls through to the regular flag parser. Mirrors
// the dispatch shape of runUpdateCommand / runExtCommand so the router
// in cli.go stays uniform.
func runModelsCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "models" {
		return false, nil
	}
	rest := rawArgs[1:]
	if len(rest) == 0 {
		printModelsHelp()
		return true, nil
	}
	switch rest[0] {
	case "-h", "--help", "help":
		printModelsHelp()
		return true, nil
	case "init":
		force := false
		for _, a := range rest[1:] {
			switch a {
			case "-f", "--force":
				force = true
			case "-h", "--help":
				printModelsHelp()
				return true, nil
			default:
				printModelsHelp()
				return true, fmt.Errorf("unknown flag for `models init`: %s", a)
			}
		}
		return true, runModelsInit(force)
	case "endpoints":
		apply := false
		for _, a := range rest[1:] {
			switch a {
			case "--apply":
				apply = true
			case "-h", "--help":
				printModelsHelp()
				return true, nil
			default:
				printModelsHelp()
				return true, fmt.Errorf("unknown flag for `models endpoints`: %s", a)
			}
		}
		return true, runModelsEndpoints(apply)
	case "tiers":
		all := false
		for _, a := range rest[1:] {
			switch a {
			case "--all":
				all = true
			case "-h", "--help":
				printModelsHelp()
				return true, nil
			default:
				printModelsHelp()
				return true, fmt.Errorf("unknown flag for `models tiers`: %s", a)
			}
		}
		return true, runModelsTiers(all)
	default:
		printModelsHelp()
		return true, fmt.Errorf("unknown models subcommand: %s", rest[0])
	}
}

func printModelsHelp() {
	fmt.Fprintln(os.Stderr, `terva models — manage your custom model catalog ($TERVA_HOME/models.json)

usage:
  terva models init            scaffold a models.json you can edit
  terva models init --force     overwrite an existing models.json
  terva models endpoints        suggest config.json named endpoints from
                                your models.json baseUrl entries
  terva models endpoints --apply  add those endpoints to config.json
  terva models tiers            show the weak/medium/strong sub-agent model
                                each provider resolves to (and how to set it)
  terva models tiers --all      include providers you're not logged into
  terva models help             show this help

notes:
  * models.json lets you register models that aren't in the built-in
    catalog, or pin exact context/max-token sizes for local and
    OpenAI-compatible endpoints. See docs/providers.md.
  * Each entry's "baseUrl" pins that model to a specific endpoint, so a
    single models.json can point at several servers at once — unlike
    '/login', which stores only one openai-compatible endpoint.
  * After editing, run 'terva --list-models' to confirm your entries load
    (they show source: user).`)
}

// runModelsInit writes the starter scaffold to $TERVA_HOME/models.json.
// It refuses to clobber an existing file unless force is set, so a user
// who already has a hand-tuned catalog can't lose it to a stray
// `terva models init`.
func runModelsInit(force bool) error {
	path := UserModelsPath()
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("models.json already exists at %s (pass --force to overwrite)", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(modelsScaffold), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("wrote %s\n", path)
	fmt.Println("  edit it to add your endpoints, then run `terva --list-models` to verify")
	return nil
}

// runModelsEndpoints inspects models.json for OpenAI-compatible entries that
// pin a baseUrl (the pre-named-endpoints multi-backend pattern) and proposes a
// config.json "endpoints" block — one named endpoint per distinct base URL, so
// per-endpoint discovery can replace the hand-listed models. With apply it
// merges the endpoints into config.json (additive; never clobbers an existing
// one). The now-redundant models.json entries are only FLAGGED, never deleted —
// the user decides what to keep for per-model overrides.
//
// The proposed JSON goes to stdout (pipeable); guidance goes to stderr.
func runModelsEndpoints(apply bool) error {
	path := UserModelsPath()
	f, err := provider.ReadUserModelsFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Distinct base URLs (first-seen order) + the models pinned to each.
	type epInfo struct {
		ctx    int
		models []string
	}
	var order []string
	byURL := map[string]*epInfo{}
	for _, m := range f.Providers["openai-compatible"].Models {
		bu := strings.TrimSpace(m.BaseURL)
		if bu == "" {
			continue
		}
		e := byURL[bu]
		if e == nil {
			e = &epInfo{}
			byURL[bu] = e
			order = append(order, bu)
		}
		if m.ContextWindow > e.ctx {
			e.ctx = m.ContextWindow
		}
		e.models = append(e.models, m.ID)
	}
	if len(order) == 0 {
		fmt.Fprintf(os.Stderr, "No OpenAI-compatible models with a baseUrl found in %s.\n", path)
		fmt.Fprintln(os.Stderr, "(Nothing to migrate — named endpoints are for the multi-baseUrl models.json pattern.)")
		return nil
	}

	// Name each endpoint from its host, avoiding collisions with built-in
	// providers and with each other.
	used := map[string]bool{}
	eps := map[string]EndpointConfig{}
	nameFor := map[string]string{}
	for _, bu := range order {
		name := endpointNameFor(bu, used)
		nameFor[bu] = name
		eps[name] = EndpointConfig{BaseURL: bu, ContextWindow: byURL[bu].ctx}
	}

	fmt.Fprintf(os.Stderr, "Found %d OpenAI-compatible endpoint(s) in %s.\n\n", len(order), path)

	if apply {
		added, err := mergeEndpointsIntoConfig(eps)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Added %d endpoint(s) to %s. Relaunch to discover their models.\n\n", added, ConfigPath())
	} else {
		block, _ := json.MarshalIndent(map[string]any{"endpoints": eps}, "", "  ")
		fmt.Fprintf(os.Stderr, "Add this to %s (or run with --apply):\n\n", ConfigPath())
		fmt.Println(string(block)) // stdout: the pasteable block
		fmt.Fprintln(os.Stderr)
	}

	fmt.Fprintln(os.Stderr, "These models.json entries become redundant once the endpoints exist")
	fmt.Fprintln(os.Stderr, "(discovery lists them) — remove the ones you don't need for per-model overrides:")
	for _, bu := range order {
		for _, id := range byURL[bu].models {
			fmt.Fprintf(os.Stderr, "  - openai-compatible/%s  (%s → endpoint %q)\n", id, bu, nameFor[bu])
		}
	}
	if !apply {
		fmt.Fprintln(os.Stderr, "\nRe-run with --apply to add the endpoints to config.json automatically")
		fmt.Fprintln(os.Stderr, "(models.json is left untouched for you to trim).")
	}
	return nil
}

// runModelsTiers prints the weak/medium/strong sub-agent model each provider
// resolves to for swarm_spawn's `tier` parameter, and — for providers with no
// mapping (gateways terva can't guess) — the config.json block to set one.
// Resolution is shown UNCAPPED; at spawn time a tier is capped to the host
// model's own strength.
func runModelsTiers(all bool) error {
	cfg, _ := LoadConfig()
	overrides := swarmTierMap(cfg.SwarmTiers)

	// Which providers to show: logged-in (default) or every known provider.
	// "Logged in" = a resolvable credential, plus ollama (needs no auth).
	show := map[string]bool{}
	for _, p := range knownProviders {
		if all {
			show[p] = true
			continue
		}
		if _, _, err := ResolveCredential(p, ""); err == nil {
			show[p] = true
		}
	}
	show["ollama"] = true
	// Always include a provider the user has explicitly configured tiers for.
	for p := range overrides {
		show[p] = true
	}

	// Stable order: knownProviders first (registry order), then extras sorted.
	var order []string
	seen := map[string]bool{}
	for _, p := range knownProviders {
		if show[p] {
			order = append(order, p)
			seen[p] = true
		}
	}
	var extra []string
	for p := range show {
		if !seen[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)

	ranks := tools.SwarmRankNames()
	fmt.Println("Swarm sub-agent tiers — the model `swarm_spawn` resolves for each `tier`.")
	fmt.Println("(shown uncapped; at spawn time a tier is capped to the host model's own strength)")
	fmt.Println()

	for _, p := range order {
		models, sources := tools.SwarmTierLadder(p, overrides)
		mapped := models[0] != "" || models[1] != "" || models[2] != ""
		header := p
		if !mapped {
			header += "   (no tier mapping — `tier` is ignored; sub-agents use the host model)"
		}
		fmt.Println(header)

		catalog := map[string]bool{}
		for _, m := range provider.ModelsForProvider(p) {
			catalog[m.ID] = true
		}
		for i, name := range ranks {
			id := models[i]
			if id == "" {
				fmt.Printf("  %-7s —\n", name)
				continue
			}
			note := sources[i]
			if sources[i] == "override" && len(catalog) > 0 && !catalog[id] {
				note += ", NOT in this provider's catalog"
			}
			fmt.Printf("  %-7s %-30s %s\n", name, id, note)
		}
		if !mapped {
			printTierScaffold(p)
		}
		fmt.Println()
	}
	fmt.Printf("Configure per provider in %s under \"swarm_tiers\". See docs/models.md.\n", ConfigPath())
	return nil
}

// printTierScaffold shows the config block to pin tiers for a provider that has
// none, plus a few candidate model ids from its catalog to choose from.
func printTierScaffold(providerID string) {
	fmt.Printf("  pin them in %s:\n", ConfigPath())
	fmt.Printf("    \"swarm_tiers\": { %q: { \"weak\": \"...\", \"medium\": \"...\", \"strong\": \"...\" } }\n", providerID)
	var ids []string
	for _, m := range provider.ModelsForProvider(providerID) {
		ids = append(ids, m.ID)
		if len(ids) >= 8 {
			break
		}
	}
	if len(ids) > 0 {
		fmt.Printf("  candidates: %s  (`terva --list-models` for the full list)\n", strings.Join(ids, ", "))
	}
}

// endpointNameFor derives a stable, valid endpoint id from a base URL's host,
// avoiding collisions with built-in provider ids and with names already used.
func endpointNameFor(rawURL string, used map[string]bool) string {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
		switch host {
		case "localhost", "127.0.0.1", "0.0.0.0":
			if p := u.Port(); p != "" {
				host = "local-" + p
			} else {
				host = "local"
			}
		}
	}
	name := sanitizeID(host)
	if name == "" {
		name = "endpoint"
	}
	if _, builtIn := providerByID[name]; builtIn {
		name += "-ep" // never propose a name that collides with a built-in provider
	}
	base := name
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	used[name] = true
	return name
}

// sanitizeID lowercases s and keeps [a-z0-9-], turning separators into dashes.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == ' ' || r == ':':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// mergeEndpointsIntoConfig adds eps to config.json's endpoints map, skipping any
// name that already exists (never clobbers). Returns how many were added.
func mergeEndpointsIntoConfig(eps map[string]EndpointConfig) (int, error) {
	cfg, _ := LoadConfig()
	if cfg.Endpoints == nil {
		cfg.Endpoints = map[string]EndpointConfig{}
	}
	added := 0
	for name, ep := range eps {
		if _, exists := cfg.Endpoints[name]; exists {
			continue
		}
		cfg.Endpoints[name] = ep
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if err := SaveConfig(cfg); err != nil {
		return 0, err
	}
	return added, nil
}
