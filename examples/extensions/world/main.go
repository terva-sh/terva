// world — a generic, procedurally-generated place an agent can explore.
//
// This example demonstrates that a terva extension can be a *world*, not just
// a tool: its tools are the agent's senses and effectors, its persisted state
// is the world's memory, and its live context card is the agent's awareness of
// its surroundings. The extension is the referee (it owns what is true); the
// agent (in a persona) is the explorer who perceives, decides, and acts.
//
// It is deliberately generic — a wilderness of forests, ridges, marshes and
// ruins — so you can lift the shape for any simulator: a dungeon, a market, a
// building, a star system.
//
// Things worth copying:
//   - a deterministic procedural world (same seed ⇒ same map), so the fiction
//     stays coherent across turns and sessions;
//   - persisted, consequential state (stamina, what you've gathered, where
//     you've been) via the SDK DataFS;
//   - a live context card pushed every turn (the agent's proprioception), a
//     status-line segment, and a fog-of-war map panel (/map);
//   - ext.Sequential() on the effectors (travel/interact/rest) so calls that
//     depend on order — "travel north, then gather what you see there" — are
//     applied in the order the agent issued them, even when the model emits
//     them together. Read-only tools (observe/inventory/status) stay concurrent.
//
// Run it from the repo (no build step needed):
//
//	terva --ext ./examples/extensions/world --persona ./examples/extensions/world/personas/wayfarer.md
//
// then: "Where am I? Look around, then head toward higher ground."
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/ext"
)

const (
	stateFile = "world.json"
	panelID   = "world-map"
	cardID    = "surroundings"
	statusID  = "world"
	cardLabel = "YOUR SURROUNDINGS (live — your ground truth)"
	gridMin   = 0
	gridMax   = 7 // an 8x8 region grid
)

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type pos struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func (p pos) key() string { return fmt.Sprintf("%d,%d", p.X, p.Y) }

type being struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // animal | person
	Mood string `json:"mood"` // friendly | wary | indifferent | hostile
}

// region is the contents of one grid cell. Generated deterministically from
// the seed + coordinates, then filtered by what the explorer has already taken.
type region struct {
	Terrain  string   `json:"terrain"`
	Desc     string   `json:"desc"`
	Features []string `json:"features"`
	Items    []string `json:"items"`
	Beings   []being  `json:"beings"`
}

type journalEntry struct {
	Day  int    `json:"day"`
	Text string `json:"text"`
}

// state is the whole simulation, persisted to world.json.
type state struct {
	Pos       pos             `json:"pos"`
	Stamina   int             `json:"stamina"` // 0-100
	Minutes   int             `json:"minutes"` // elapsed since the journey began
	Inventory []string        `json:"inventory"`
	Visited   map[string]bool `json:"visited"` // fog-of-war, by pos.key()
	Taken     map[string]bool `json:"taken"`   // gathered items, by "x,y:item"
	Here      region          `json:"here"`    // current region (regenerated on travel)
	Journal   []journalEntry  `json:"journal"`
}

func newState() state {
	return state{
		Pos:     pos{3, 6}, // start near the south edge, looking north
		Stamina: 100,
		Minutes: 6 * 60, // dawn
		Visited: map[string]bool{},
		Taken:   map[string]bool{},
	}
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

type app struct {
	e         *ext.Extension
	mu        sync.Mutex
	st        state
	loaded    bool
	panelOpen bool
	fs        ext.DataFS
}

func main() {
	e := ext.New("world", "1.0.0")
	a := &app{e: e}

	e.OnSession(func(ext.Session) {
		a.ensureLoaded()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.refreshSurfacesLocked()
	})

	a.register()

	if err := e.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "world:", err)
		os.Exit(1)
	}
}

func (a *app) worldSeed() string {
	if s := strings.TrimSpace(a.e.Config().String("seed")); s != "" {
		return s
	}
	return "wayfarer"
}

func (a *app) ensureLoaded() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loaded {
		return
	}
	a.fs = a.e.Host().DataFS()
	a.loaded = true
	b, err := a.fs.ReadFile(stateFile)
	if err != nil || len(b) == 0 {
		a.st = newState()
		a.st.Here = a.generate(a.st.Pos)
		a.st.Visited[a.st.Pos.key()] = true
		a.saveLocked()
		return
	}
	if err := json.Unmarshal(b, &a.st); err != nil {
		a.e.Logf("corrupt %s (%v) — starting fresh", stateFile, err)
		a.st = newState()
		a.st.Here = a.generate(a.st.Pos)
		a.st.Visited[a.st.Pos.key()] = true
	}
	if a.st.Visited == nil {
		a.st.Visited = map[string]bool{}
	}
	if a.st.Taken == nil {
		a.st.Taken = map[string]bool{}
	}
	a.saveLocked()
}

func (a *app) saveLocked() {
	b, err := json.MarshalIndent(a.st, "", "  ")
	if err != nil {
		a.e.Logf("marshal state: %v", err)
		return
	}
	if err := a.fs.WriteFile(stateFile, b, 0o644); err != nil {
		a.e.Logf("save state: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Deterministic world generation
// ---------------------------------------------------------------------------

func (a *app) rng(p pos, salt string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(a.worldSeed() + salt))
	_ = binary.Write(h, binary.LittleEndian, int64(p.X))
	_ = binary.Write(h, binary.LittleEndian, int64(p.Y))
	return rand.New(rand.NewSource(int64(h.Sum64())))
}

var terrains = []struct{ name, desc string }{
	{"old-growth forest", "Tall pines close overhead; the light comes down green and the floor is soft with needles."},
	{"wind-scoured ridge", "Bare rock and low scrub, the wind constant. The land falls away on every side."},
	{"reed marsh", "Standing water between hummocks of reed; the air smells of cold mud and decay."},
	{"sunken ruins", "Toppled walls of cut stone, half-swallowed by moss and root. Something was here, long ago."},
	{"river ford", "A wide shallow run of water over pale stones; the far bank is a low cut of clay."},
	{"wildflower meadow", "Open grass scattered with late blooms, loud with insects, warm where the sun reaches."},
	{"mossy hollow", "A sheltered dip thick with fern and deadfall; sound seems to stop at its edges."},
	{"pebbled shore", "A long grey beach of round stones along still water, driftwood bleached at the tideline."},
	{"birch stand", "Slim white trunks in loose ranks, leaves turning, the ground dappled and bright."},
	{"boulder field", "A chaos of house-sized stones with narrow ways between; easy to lose your bearings."},
}

var featurePool = []string{
	"a moss-covered cairn", "a cold spring", "a leaning standing-stone", "a hollow log",
	"fresh tracks in the mud", "a weathered signpost, its words worn away", "the cold embers of an old fire",
	"a tangle of brambles", "a half-buried door in the hillside", "a rope bridge, frayed but holding",
}

var itemPool = []string{
	"a handful of brambleberries", "a sprig of yarrow", "a smooth river stone", "a curl of fallen birchbark",
	"a flint shard", "a long grey feather", "a wild apple", "a coil of weathered rope",
}

var animals = []string{"a watchful fox", "a grazing deer", "a circling hawk", "a startled hare", "a pair of ravens"}
var folk = []string{"a hooded wanderer", "an old forager with a full basket", "a lost-looking child", "a wary trapper"}
var moods = []string{"friendly", "wary", "indifferent", "hostile"}

// consumables restore stamina when used.
var consumables = map[string]int{
	"a handful of brambleberries": 15,
	"a wild apple":                20,
}

func (a *app) generate(p pos) region {
	r := a.rng(p, "region")
	t := terrains[r.Intn(len(terrains))]
	reg := region{Terrain: t.name, Desc: t.desc}

	for i := 0; i < r.Intn(3); i++ { // 0-2 features
		reg.Features = appendUnique(reg.Features, featurePool[r.Intn(len(featurePool))])
	}
	for i := 0; i < r.Intn(3); i++ { // 0-2 items
		it := itemPool[r.Intn(len(itemPool))]
		if a.st.Taken[p.key()+":"+it] { // already gathered on a prior visit
			continue
		}
		reg.Items = appendUnique(reg.Items, it)
	}
	if r.Intn(2) == 0 { // ~half have an inhabitant
		if r.Intn(3) == 0 {
			reg.Beings = []being{{Name: folk[r.Intn(len(folk))], Kind: "person", Mood: moods[r.Intn(len(moods))]}}
		} else {
			reg.Beings = []being{{Name: animals[r.Intn(len(animals))], Kind: "animal", Mood: moods[r.Intn(len(moods))]}}
		}
	}
	return reg
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// ---------------------------------------------------------------------------
// Time & presentation
// ---------------------------------------------------------------------------

func (s state) day() int { return s.Minutes/(24*60) + 1 }

func (s state) timeOfDay() string {
	m := s.Minutes % (24 * 60)
	switch {
	case m < 5*60:
		return "night"
	case m < 8*60:
		return "dawn"
	case m < 12*60:
		return "morning"
	case m < 14*60:
		return "midday"
	case m < 18*60:
		return "afternoon"
	case m < 20*60:
		return "dusk"
	default:
		return "night"
	}
}

func exitsFrom(p pos) []string {
	var out []string
	if p.Y > gridMin {
		out = append(out, "north")
	}
	if p.Y < gridMax {
		out = append(out, "south")
	}
	if p.X < gridMax {
		out = append(out, "east")
	}
	if p.X > gridMin {
		out = append(out, "west")
	}
	return out
}

func (a *app) cardTextLocked() string {
	s := a.st
	var b strings.Builder
	fmt.Fprintf(&b, "Location: (%d,%d) — %s\n", s.Pos.X, s.Pos.Y, s.Here.Terrain)
	fmt.Fprintf(&b, "%s\n", s.Here.Desc)
	fmt.Fprintf(&b, "Time: %s, day %d · Stamina: %d/100\n", s.timeOfDay(), s.day(), s.Stamina)
	if len(s.Here.Features) > 0 {
		fmt.Fprintf(&b, "You see: %s\n", strings.Join(s.Here.Features, "; "))
	}
	if len(s.Here.Items) > 0 {
		fmt.Fprintf(&b, "Within reach: %s\n", strings.Join(s.Here.Items, "; "))
	}
	for _, bg := range s.Here.Beings {
		fmt.Fprintf(&b, "Present: %s (%s, seems %s)\n", bg.Name, bg.Kind, bg.Mood)
	}
	fmt.Fprintf(&b, "Ways on: %s\n", strings.Join(exitsFrom(s.Pos), ", "))
	if len(s.Inventory) > 0 {
		fmt.Fprintf(&b, "Carrying: %s", strings.Join(s.Inventory, ", "))
	} else {
		b.WriteString("Carrying: nothing")
	}
	return b.String()
}

func (a *app) statusSegLocked() string {
	return fmt.Sprintf("🧭 (%d,%d) %s · %s d%d · stamina %d%%",
		a.st.Pos.X, a.st.Pos.Y, a.st.Here.Terrain, a.st.timeOfDay(), a.st.day(), a.st.Stamina)
}

func (a *app) refreshSurfacesLocked() {
	a.e.SetContextCard(cardID, cardLabel, a.cardTextLocked())
	a.e.SetStatus(statusID, a.statusSegLocked())
	if a.panelOpen {
		a.e.RenderPanel(panelID, "Map", a.mapLinesLocked(), "r refresh · esc close")
	}
}

func (a *app) mapLinesLocked() []string {
	lines := []string{fmt.Sprintf("  day %d, %s — stamina %d/100", a.st.day(), a.st.timeOfDay(), a.st.Stamina), ""}
	for y := gridMin; y <= gridMax; y++ {
		var row strings.Builder
		row.WriteString("  ")
		for x := gridMin; x <= gridMax; x++ {
			switch {
			case x == a.st.Pos.X && y == a.st.Pos.Y:
				row.WriteString("@ ")
			case a.st.Visited[pos{x, y}.key()]:
				row.WriteString("· ")
			default:
				row.WriteString("░ ")
			}
		}
		lines = append(lines, row.String())
	}
	lines = append(lines, "", "  @ you · · visited · ░ unknown")
	return lines
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

func mustSchema(v map[string]any) json.RawMessage { b, _ := json.Marshal(v); return b }

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func (a *app) register() {
	e := a.e

	e.Tool("observe",
		"Look around your current location: terrain, what you can see and reach, who's present, and the ways onward. Side-effect free.",
		mustSchema(obj(map[string]any{})),
		a.toolObserve, ext.ReadOnly())

	e.Tool("travel",
		"Travel one region in a compass direction (north/south/east/west). Costs stamina and time, and brings you to a new place to observe. Order-sensitive: you must arrive before you can act on the new region.",
		mustSchema(obj(map[string]any{
			"direction": map[string]any{"type": "string", "enum": []string{"north", "south", "east", "west"}},
		}, "direction")),
		a.toolTravel, ext.Sequential())

	e.Tool("interact",
		"Act on something here: examine a feature, gather an item into your pack, use an item you carry, or talk to someone present. Order-sensitive (you can only gather what's here now, and only use what you've already gathered).",
		mustSchema(obj(map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"examine", "gather", "use", "talk"}},
			"target": map[string]any{"type": "string", "description": "What to act on — a feature, item, or being named in your surroundings."},
		}, "action", "target")),
		a.toolInteract, ext.Sequential())

	e.Tool("rest",
		"Make camp and rest to recover stamina. Costs a stretch of time. Order-sensitive relative to travel and interaction.",
		mustSchema(obj(map[string]any{})),
		a.toolRest, ext.Sequential())

	e.Tool("inventory",
		"List what you are carrying. Side-effect free.",
		mustSchema(obj(map[string]any{})),
		a.toolInventory, ext.ReadOnly())

	e.Tool("status",
		"A full report of where you are, your condition, and your surroundings. Side-effect free.",
		mustSchema(obj(map[string]any{})),
		a.toolStatus, ext.ReadOnly())

	e.Tool("journal",
		"Record a journal entry — an observation, a decision, a memory of the road. Review the journal with the /journal command.",
		mustSchema(obj(map[string]any{
			"entry": map[string]any{"type": "string", "description": "The entry text, in your own voice."},
		}, "entry")),
		a.toolJournal, ext.WithAuthority(ext.AuthorityLocalData))

	e.Command("map", "show the explored map", func(string) ext.Response {
		a.ensureLoaded()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.panelOpen = true
		return ext.OpenPanel(panelID, "Map", a.mapLinesLocked(), "r refresh · esc close")
	})
	e.OnPanelKey(panelID, func(string, string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.e.RenderPanel(panelID, "Map", a.mapLinesLocked(), "r refresh · esc close")
	}, func() {
		a.mu.Lock()
		a.panelOpen = false
		a.mu.Unlock()
	})

	e.Command("journal", "read your journal", func(string) ext.Response {
		a.ensureLoaded()
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.st.Journal) == 0 {
			return ext.Display("Your journal is empty.")
		}
		var b strings.Builder
		b.WriteString("Journal\n")
		for _, je := range a.st.Journal {
			fmt.Fprintf(&b, "  Day %d — %s\n", je.Day, je.Text)
		}
		return ext.Display(strings.TrimRight(b.String(), "\n"))
	})
}

func (a *app) toolObserve(json.RawMessage) ext.ToolResult {
	a.ensureLoaded()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshSurfacesLocked()
	return ext.TextResult(a.cardTextLocked())
}

var dirDelta = map[string]pos{
	"north": {0, -1}, "south": {0, 1}, "east": {1, 0}, "west": {-1, 0},
}

func clamp(n int) int {
	if n < gridMin {
		return gridMin
	}
	if n > gridMax {
		return gridMax
	}
	return n
}

func (a *app) toolTravel(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Direction string `json:"direction"`
	}
	_ = json.Unmarshal(raw, &in)
	a.ensureLoaded()
	a.mu.Lock()
	defer a.mu.Unlock()

	d, ok := dirDelta[strings.ToLower(strings.TrimSpace(in.Direction))]
	if !ok {
		return ext.TextErrorResult("Choose a direction: north, south, east, or west.")
	}
	dest := pos{clamp(a.st.Pos.X + d.X), clamp(a.st.Pos.Y + d.Y)}
	if dest == a.st.Pos {
		return ext.TextErrorResult("The way " + in.Direction + " is blocked — you're at the edge of the known land.")
	}

	// Stamina cost varies a little by where you're going.
	cost := 10 + a.rng(dest, "cost").Intn(9) // 10-18
	if a.st.Stamina < cost {
		return ext.TextErrorResult(fmt.Sprintf("You are too tired to travel (stamina %d, need ~%d). Rest first.", a.st.Stamina, cost))
	}
	a.st.Stamina -= cost
	a.st.Minutes += 30 + a.rng(dest, "time").Intn(60) // 30-89 min
	a.st.Pos = dest
	a.st.Here = a.generate(dest)
	a.st.Visited[dest.key()] = true
	a.saveLocked()
	a.refreshSurfacesLocked()

	var b strings.Builder
	fmt.Fprintf(&b, "You travel %s to (%d,%d) — %s. (Stamina %d/100, %s of day %d.)\n",
		in.Direction, dest.X, dest.Y, a.st.Here.Terrain, a.st.Stamina, a.st.timeOfDay(), a.st.day())
	b.WriteString(a.cardTextLocked())
	return ext.TextResult(b.String())
}

func (a *app) toolInteract(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Action string `json:"action"`
		Target string `json:"target"`
	}
	_ = json.Unmarshal(raw, &in)
	a.ensureLoaded()
	a.mu.Lock()
	defer a.mu.Unlock()

	target := strings.TrimSpace(in.Target)
	switch strings.ToLower(in.Action) {
	case "examine":
		if m := matchOne(a.st.Here.Features, target); m != "" {
			return ext.TextResult(examineFeature(m))
		}
		if m := matchOne(a.st.Here.Items, target); m != "" {
			return ext.TextResult("You look closely at " + m + ". You could gather it.")
		}
		for _, bg := range a.st.Here.Beings {
			if containsFold(bg.Name, target) {
				return ext.TextResult(fmt.Sprintf("%s — %s. It seems %s.", bg.Name, bg.Kind, bg.Mood))
			}
		}
		return ext.TextErrorResult("There is no \"" + target + "\" here to examine. (observe to see what's around.)")

	case "gather":
		m := matchOne(a.st.Here.Items, target)
		if m == "" {
			return ext.TextErrorResult("There is no \"" + target + "\" within reach to gather.")
		}
		a.st.Here.Items = remove(a.st.Here.Items, m)
		a.st.Inventory = append(a.st.Inventory, m)
		a.st.Taken[a.st.Pos.key()+":"+m] = true
		a.st.Minutes += 5
		a.saveLocked()
		a.refreshSurfacesLocked()
		return ext.TextResult("You gather " + m + " and stow it in your pack.")

	case "use":
		m := matchOne(a.st.Inventory, target)
		if m == "" {
			return ext.TextErrorResult("You are not carrying \"" + target + "\".")
		}
		if gain, ok := consumables[m]; ok {
			a.st.Inventory = remove(a.st.Inventory, m)
			before := a.st.Stamina
			a.st.Stamina = minInt(100, a.st.Stamina+gain)
			a.st.Minutes += 5
			a.saveLocked()
			a.refreshSurfacesLocked()
			return ext.TextResult(fmt.Sprintf("You eat %s. Stamina %d → %d.", m, before, a.st.Stamina))
		}
		return ext.TextResult("You turn " + m + " over in your hands. It may be useful later, but not as food.")

	case "talk":
		for _, bg := range a.st.Here.Beings {
			if containsFold(bg.Name, target) {
				if bg.Kind == "animal" {
					return ext.TextResult(fmt.Sprintf("%s cannot speak, but it watches you (%s). Narrate the moment in character.", bg.Name, bg.Mood))
				}
				return ext.TextResult(fmt.Sprintf("GROUND TRUTH: %s is a person, mood=%s. Improvise their reply in character, consistent with that mood.", bg.Name, bg.Mood))
			}
		}
		return ext.TextErrorResult("There is no one called \"" + target + "\" here to talk to.")

	default:
		return ext.TextErrorResult("Unknown action. Use examine, gather, use, or talk.")
	}
}

func (a *app) toolRest(json.RawMessage) ext.ToolResult {
	a.ensureLoaded()
	a.mu.Lock()
	defer a.mu.Unlock()
	before := a.st.Stamina
	a.st.Stamina = minInt(100, a.st.Stamina+35)
	a.st.Minutes += 120
	a.saveLocked()
	a.refreshSurfacesLocked()
	return ext.TextResult(fmt.Sprintf("You make camp and rest. Stamina %d → %d. It is now %s of day %d.",
		before, a.st.Stamina, a.st.timeOfDay(), a.st.day()))
}

func (a *app) toolInventory(json.RawMessage) ext.ToolResult {
	a.ensureLoaded()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.st.Inventory) == 0 {
		return ext.TextResult("Your pack is empty.")
	}
	return ext.TextResult("You are carrying: " + strings.Join(a.st.Inventory, ", ") + ".")
}

func (a *app) toolStatus(json.RawMessage) ext.ToolResult {
	a.ensureLoaded()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshSurfacesLocked()
	return ext.TextResult(a.cardTextLocked())
}

func (a *app) toolJournal(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Entry string `json:"entry"`
	}
	_ = json.Unmarshal(raw, &in)
	entry := strings.TrimSpace(in.Entry)
	if entry == "" {
		return ext.TextErrorResult("a journal entry is required.")
	}
	a.ensureLoaded()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.Journal = append(a.st.Journal, journalEntry{Day: a.st.day(), Text: entry})
	a.saveLocked()
	return ext.TextResult(fmt.Sprintf("Journal entry recorded (day %d).", a.st.day()))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func examineFeature(f string) string {
	details := map[string]string{
		"a moss-covered cairn":                      "A heap of stacked stones, deliberately built. A trail marker, or a grave. Someone passed this way.",
		"a cold spring":                             "Clear water wells up between rocks, achingly cold. Safe to drink; you could refill here.",
		"a leaning standing-stone":                  "A single tall stone, tilted by centuries. Faint marks on its face, too worn to read.",
		"a hollow log":                              "A fallen trunk gone hollow. Something small has been nesting inside.",
		"fresh tracks in the mud":                   "Prints, recent — a large animal, or a person in heavy boots, heading north.",
		"a weathered signpost, its words worn away": "A signpost at a fork that no longer is. Whatever it pointed to is lost.",
		"the cold embers of an old fire":            "A campfire, days dead. Someone sheltered here not long ago.",
		"a tangle of brambles":                      "A dense thorny snarl. Slow to push through, but berries grow in it.",
		"a half-buried door in the hillside":        "A wooden door set into the slope, half-sunk in earth. It does not want to open.",
		"a rope bridge, frayed but holding":         "A swaying span over a drop. It would bear you, carefully.",
	}
	if d, ok := details[f]; ok {
		return d
	}
	return "You examine " + f + " closely, and fix it in memory."
}

func matchOne(pool []string, target string) string {
	t := strings.ToLower(strings.TrimSpace(target))
	if t == "" {
		return ""
	}
	for _, x := range pool {
		if strings.ToLower(x) == t {
			return x
		}
	}
	for _, x := range pool { // looser: substring either way
		lx := strings.ToLower(x)
		if strings.Contains(lx, t) || strings.Contains(t, lx) {
			return x
		}
	}
	return ""
}

func containsFold(s, sub string) bool {
	sub = strings.TrimSpace(sub)
	return sub != "" && strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func remove(s []string, v string) []string {
	for i, x := range s {
		if x == v {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
