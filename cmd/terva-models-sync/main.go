// Command terva-models-sync reconciles the baked model catalog against
// models.dev — the community catalog opencode (and others) publish from — and
// against what each provider's /v1/models endpoint actually serves.
//
// Why this exists. A provider's /v1/models list is authoritative about WHICH
// models exist but says nothing about their limits: the OpenAI list schema
// carries only {id, object, created, owned_by}. So a model the baked catalog
// has never heard of gets discovered, has no context window to report, and
// falls back to a guessed default — which is how a million-token model ends up
// budgeted as 32k. models.dev carries the limits and prices; this tool brings
// the two together at build time so the shipped binary needs no network to
// know how big a model's context is.
//
// It fills gaps; it does not clobber. models.dev is NOT a superset of what the
// providers serve (several opencode-go models are absent from it, and the baked
// catalog carries hand-curated limits for some of those), so an entry models.dev
// knows nothing about is left exactly as it is. Where both sources have a value
// and they disagree, the tool reports the drift and — with -write — takes
// models.dev's, on the grounds that it tracks upstream and a hand-typed constant
// does not. The result is a reviewable diff, which is the point of doing this at
// build time rather than at runtime.
//
//	go run ./cmd/terva-models-sync          # report only
//	go run ./cmd/terva-models-sync -write   # rewrite the catalog block
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	modelsDevURL   = "https://models.dev/api.json"
	catalogPath    = "packages/provider/catalog_builtin.go"
	requestTimeout = 45 * time.Second
)

// target is one terva provider whose catalog block this tool owns.
//
// membershipURL decides WHICH models the provider serves — models.dev groups
// every opencode tier under a single key, so it cannot tell the zen tier from
// the zen/go tier and only the live endpoint can. metaKey is where the limits
// and prices come from.
type target struct {
	provider      string // terva provider id, and the catalog block's marker
	metaKey       string // models.dev top-level provider key
	membershipURL string // the provider's own /v1/models
	baseURL       string // BaseURL stamped on every entry (uniform by construction)
}

var targets = []target{
	{
		provider:      "opencode-go",
		metaKey:       "opencode",
		membershipURL: "https://opencode.ai/zen/go/v1/models",
		baseURL:       "https://opencode.ai/zen/go/v1",
	},
}

// ---- models.dev shapes (only the fields we consume) ----

type mdCatalog map[string]struct {
	Models map[string]mdModel `json:"models"`
}

type mdModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Reasoning bool   `json:"reasoning"`
	Limit     struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
}

func (m mdModel) vision() bool {
	for _, in := range m.Modalities.Input {
		if in == "image" {
			return true
		}
	}
	return false
}

// entry is one catalog row, however it was sourced.
type entry struct {
	ID          string
	DisplayName string
	Context     int
	MaxOutput   int
	Reasoning   bool
	Vision      bool
	In, Out     float64
	CacheRead   float64
	CacheWrite  float64
	// known is false when neither models.dev nor the baked catalog can say how
	// big this model's context is. Such a model still runs — it just falls back
	// to a guessed default — so it is reported loudly rather than papered over.
	known bool
}

func main() {
	write := flag.Bool("write", false, "rewrite the catalog block in place")
	flag.Parse()

	md, err := fetchModelsDev()
	if err != nil {
		die("models.dev: %v", err)
	}
	src, err := os.ReadFile(catalogPath)
	if err != nil {
		die("read catalog: %v (run from the repo root)", err)
	}
	out := string(src)
	changed := false

	for _, t := range targets {
		served, err := fetchServed(t.membershipURL)
		if err != nil {
			die("%s: list models: %v", t.provider, err)
		}
		baked := parseBaked(out, t.provider)

		merged, rep := reconcile(t, md, served, baked)
		rep.print(t.provider)

		block := render(t, merged)
		next, err := replaceBlock(out, t.provider, block)
		if err != nil {
			die("%s: %v", t.provider, err)
		}
		if next != out {
			changed = true
			out = next
		}
	}

	if !*write {
		if changed {
			fmt.Println("\nthe catalog is out of date. re-run with -write to apply.")
			os.Exit(1)
		}
		fmt.Println("\ncatalog is up to date.")
		return
	}
	if !changed {
		fmt.Println("\ncatalog is up to date; nothing written.")
		return
	}
	if err := os.WriteFile(catalogPath, []byte(out), 0o644); err != nil {
		die("write catalog: %v", err)
	}
	fmt.Printf("\nwrote %s — review the diff, then gofmt.\n", catalogPath)
}

// reconcile merges what the provider serves, what models.dev knows, and what
// the catalog already says. The provider's list decides membership; models.dev
// supplies metadata; the baked entry is the fallback for anything models.dev
// has never heard of.
func reconcile(t target, md mdCatalog, served []string, baked map[string]entry) ([]entry, report) {
	meta := md[t.metaKey].Models
	var rep report
	out := make([]entry, 0, len(served))

	for _, id := range served {
		m, inMD := meta[id]
		b, inBaked := baked[id]

		switch {
		case inMD:
			e := entry{
				ID:          id,
				DisplayName: firstNonEmpty(m.Name, b.DisplayName, id),
				Context:     m.Limit.Context,
				MaxOutput:   m.Limit.Output,
				Reasoning:   m.Reasoning,
				Vision:      m.vision(),
				In:          m.Cost.Input,
				Out:         m.Cost.Output,
				CacheRead:   m.Cost.CacheRead,
				CacheWrite:  m.Cost.CacheWrite,
				known:       m.Limit.Context > 0,
			}
			// models.dev has the model but not its limits — keep whatever the
			// catalog already knew rather than replacing a real number with 0.
			if e.Context == 0 && inBaked {
				e.Context, e.MaxOutput, e.known = b.Context, b.MaxOutput, b.known
			}
			switch {
			case !inBaked:
				rep.added = append(rep.added, e)
			case b.Context != e.Context && e.Context > 0:
				rep.drifted = append(rep.drifted, drift{id: id, was: b.Context, now: e.Context})
			}
			out = append(out, e)

		case inBaked:
			// models.dev has never heard of it, but we curated it by hand. Keep
			// it exactly as it is: this is the case that makes a blind
			// regenerate destructive.
			rep.kept = append(rep.kept, b)
			out = append(out, b)

		default:
			// Nobody knows anything. It will fall back to a guessed context
			// window at runtime, so say so out loud.
			rep.unknown = append(rep.unknown, id)
			out = append(out, entry{ID: id, DisplayName: id})
		}
	}

	// A baked entry the provider no longer serves is stale. Report it; the
	// merge already drops it at runtime (discoveryAuthoritative), so leaving it
	// in the source is just noise.
	servedSet := map[string]bool{}
	for _, id := range served {
		servedSet[id] = true
	}
	for id := range baked {
		if !servedSet[id] {
			rep.retired = append(rep.retired, id)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sort.Strings(rep.retired)
	sort.Strings(rep.unknown)
	return out, rep
}

// ---- reporting ----

type drift struct {
	id       string
	was, now int
}

type report struct {
	added   []entry
	drifted []drift
	kept    []entry
	unknown []string
	retired []string
}

func (r report) print(provider string) {
	fmt.Printf("== %s ==\n", provider)
	for _, e := range r.added {
		fmt.Printf("  + %-20s ctx=%-9d out=%-7d  (new, from models.dev)\n", e.ID, e.Context, e.MaxOutput)
	}
	for _, d := range r.drifted {
		fmt.Printf("  ~ %-20s ctx %d -> %d  (models.dev disagrees with the baked value)\n", d.id, d.was, d.now)
	}
	for _, e := range r.kept {
		fmt.Printf("  = %-20s ctx=%-9d          (kept: models.dev has no entry)\n", e.ID, e.Context)
	}
	for _, id := range r.unknown {
		fmt.Printf("  ? %-20s NO METADATA ANYWHERE — falls back to a guessed context window\n", id)
	}
	for _, id := range r.retired {
		fmt.Printf("  - %-20s no longer served by the provider (dropping)\n", id)
	}
	if len(r.added)+len(r.drifted)+len(r.unknown)+len(r.retired) == 0 {
		fmt.Println("  (no changes)")
	}
}

// ---- catalog source surgery ----

// locateBlock finds the run of entry lines for one provider: everything between
// its banner comment and the next banner. The catalog is a plain Go slice
// literal with one model per line, which is exactly why it can be rewritten
// this way. Returns the [start,end) line span of the ENTRIES (banner excluded).
//
// Go's regexp has no lookahead, and a banner-to-banner match would swallow the
// next banner, so this scans lines rather than getting clever.
func locateBlock(lines []string, provider string) (start, end int, ok bool) {
	banner := "\t// ----- " + provider + " -----"
	for i, l := range lines {
		if strings.TrimRight(l, "\r") != banner {
			continue
		}
		start = i + 1
		for j := start; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "\t// -----") {
				return start, j, true
			}
		}
		return start, len(lines), true
	}
	return 0, 0, false
}

func parseBaked(src, provider string) map[string]entry {
	out := map[string]entry{}
	lines := strings.Split(src, "\n")
	start, end, ok := locateBlock(lines, provider)
	if !ok {
		return out
	}
	idRE := regexp.MustCompile(`ID: "([^"]+)"`)
	nameRE := regexp.MustCompile(`DisplayName: "([^"]+)"`)
	ctxRE := regexp.MustCompile(`ContextWindow: (\d+)`)
	outRE := regexp.MustCompile(`MaxOutput: (\d+)`)
	for _, line := range lines[start:end] {
		id := grp(idRE, line)
		if id == "" {
			continue
		}
		e := entry{
			ID:          id,
			DisplayName: grp(nameRE, line),
			Context:     atoi(grp(ctxRE, line)),
			MaxOutput:   atoi(grp(outRE, line)),
			Reasoning:   strings.Contains(line, "Reasoning: true"),
			Vision:      strings.Contains(line, "Vision: true"),
			In:          atof(line, "PriceInput"),
			Out:         atof(line, "PriceOutput"),
			CacheRead:   atof(line, "PriceCacheRead"),
			CacheWrite:  atof(line, "PriceCacheWrite"),
		}
		e.known = e.Context > 0
		out[id] = e
	}
	return out
}

// render emits the catalog rows.
//
// A model with no context window is deliberately NOT emitted. Baking a
// ContextWindow: 0 row would be worse than having no row at all: it would shadow
// the live-discovered entry with a zero window, where today the model at least
// gets a conservative default. Left out, it keeps that behaviour — and the
// report names it so a human can supply the number.
//
// Capabilities (vision, etc.) are not emitted either: the catalog never sets
// Caps inline, resolving them from the model id and capability defaults, and a
// generator has no business being the first to disagree.
func render(t target, es []entry) string {
	var b strings.Builder
	for _, e := range es {
		if e.Context == 0 {
			continue
		}
		fmt.Fprintf(&b, "\t{Provider: %q, ID: %q, DisplayName: %q, ContextWindow: %d, MaxOutput: %d, Reasoning: %t",
			t.provider, e.ID, firstNonEmpty(e.DisplayName, e.ID), e.Context, e.MaxOutput, e.Reasoning)
		fmt.Fprintf(&b, ", PriceInput: %s, PriceOutput: %s, PriceCacheRead: %s",
			num(e.In), num(e.Out), num(e.CacheRead))
		if e.CacheWrite > 0 {
			fmt.Fprintf(&b, ", PriceCacheWrite: %s", num(e.CacheWrite))
		}
		fmt.Fprintf(&b, ", BaseURL: %q},\n", t.baseURL)
	}
	return b.String()
}

func replaceBlock(src, provider, block string) (string, error) {
	lines := strings.Split(src, "\n")
	start, end, ok := locateBlock(lines, provider)
	if !ok {
		return "", fmt.Errorf("no %q block in %s (expected a `// ----- %s -----` banner followed by another banner)", provider, catalogPath, provider)
	}
	// block is newline-terminated per entry; splitting it back into lines keeps
	// the join below from doubling the separator.
	body := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	next := make([]string, 0, len(lines)-(end-start)+len(body))
	next = append(next, lines[:start]...)
	next = append(next, body...)
	next = append(next, lines[end:]...)
	return strings.Join(next, "\n"), nil
}

// ---- fetch ----

func fetchModelsDev() (mdCatalog, error) {
	body, err := get(modelsDevURL)
	if err != nil {
		return nil, err
	}
	var c mdCatalog
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return c, nil
}

func fetchServed(url string) ([]string, error) {
	body, err := get(url)
	if err != nil {
		return nil, err
	}
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	ids := make([]string, 0, len(page.Data))
	for _, d := range page.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no models listed")
	}
	return ids, nil
}

func get(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// models.dev 403s a bare Go user agent.
	req.Header.Set("user-agent", "terva-models-sync")
	c := &http.Client{Timeout: requestTimeout}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// ---- small helpers ----

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "terva-models-sync: "+f+"\n", a...)
	os.Exit(1)
}

func grp(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func atof(line, field string) float64 {
	re := regexp.MustCompile(field + `: ([0-9.]+)`)
	m := re.FindStringSubmatch(line)
	if m == nil {
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(m[1], "%g", &f); err != nil {
		return 0
	}
	return f
}

// num renders a price the way the hand-written catalog does: no trailing zeros,
// so the generated lines don't churn against the existing ones.
func num(f float64) string {
	s := fmt.Sprintf("%g", f)
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
