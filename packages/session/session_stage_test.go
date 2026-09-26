package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core/transcriptcodec"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// fullStage sets every Stage field, so a carrier that drops one shows up.
func fullStage() Stage {
	return Stage{
		Experience:      "play",
		Card:            "card_kobeni",
		Cast:            map[string]string{"Ada": "card_ada"},
		CastModels:      map[string]CastRoute{"Ada": {Provider: "anthropic", Model: "claude-opus-5"}},
		Greeting:        2,
		Background:      "bg_rain",
		UserName:        "Kai",
		UserDescription: "a tired detective",
		UserGender:      "nonbinary",
		UserPronouns:    "they/them",
		World:           "world_noir",
	}
}

// TestFullStageSetsEveryField keeps fullStage honest: a field added to Stage
// and not to the fixture would ride every test below unchecked.
func TestFullStageSetsEveryField(t *testing.T) {
	v := reflect.ValueOf(fullStage())
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("fullStage leaves Stage.%s zero", v.Type().Field(i).Name)
		}
	}
}

// writeLegacyStageSession hand-writes a session in the pre-v5 form: the Stage
// state on the meta rows, at format v4. This build cannot produce one, so the
// back-compatibility path has no other witness. The second meta row stands for
// a later setter: it is the one the loader must fold last.
func writeLegacyStageSession(t *testing.T, dir string, stage Stage) string {
	t.Helper()
	path := filepath.Join(SessionsDir(dir, dir), "20260101-000000-legacy5.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	first := SessionMeta{
		ID:            "legacy-stage",
		CWD:           dir,
		Model:         "claude-opus-5",
		Provider:      "anthropic",
		Started:       time.Now().UTC().Add(-time.Hour),
		Version:       "0.138.0",
		FormatVersion: sessionFormatVersionLore,
		Persona:       "director",
		legacyStage:   Stage{Experience: "chat", Card: "card_old"},
	}
	last := first
	last.Note = "keep it tense"
	last.legacyStage = stage
	w := transcriptcodec.EncodeMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
		Time:    time.Now().UTC(),
	})
	var b strings.Builder
	for _, line := range []sessionLine{
		{Type: "meta", Meta: &first},
		{Type: "message", Message: &w},
		{Type: "meta", Meta: &last},
	} {
		b.Write(mustJSON(t, line))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fixture must spell the state the way an old build did: flat on the
	// meta object, not under a key of its own.
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"card":"card_old"`) {
		t.Fatalf("the legacy fixture does not carry the Stage fields flat on the meta row:\n%s", raw)
	}
	return path
}

func openStage(t *testing.T, path string) (*Session, Stage) {
	t.Helper()
	s, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, s.Stage
}

func TestALegacySessionLoadsItsStageFromTheLastMetaRow(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := writeLegacyStageSession(t, dir, fullStage())

	_, got := openStage(t, path)
	if !reflect.DeepEqual(got, fullStage()) {
		t.Errorf("loaded Stage = %+v, want %+v", got, fullStage())
	}
	read, err := ReadSessionStage(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, fullStage()) {
		t.Errorf("ReadSessionStage = %+v, want %+v", read, fullStage())
	}
	sum := describeSession(path)
	if sum.Card != "card_kobeni" || sum.Experience != "play" || sum.Background != "bg_rain" || sum.World != "world_noir" {
		t.Errorf("describe = card %q experience %q background %q world %q, want the last meta row's",
			sum.Card, sum.Experience, sum.Background, sum.World)
	}
}

// A pre-v5 meta row with no Stage fields means the state was cleared, exactly
// as it did when the fields lived there.
func TestALegacyMetaRowWithoutStageClearsIt(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := writeLegacyStageSession(t, dir, Stage{})
	if _, got := openStage(t, path); !got.IsZero() {
		t.Errorf("a legacy session whose last meta row carries no Stage loaded %+v", got)
	}
}

// Below v5 every meta row is the Stage snapshot, so a meta-only setter on an
// unmigrated session must write the state again, or the next load reads its
// silence as a clear.
func TestAMetaWriteBelowV5KeepsTheStage(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := writeLegacyStageSession(t, dir, fullStage())
	s, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetNote("a new note"); err != nil {
		t.Fatal(err)
	}
	if s.Meta.FormatVersion != sessionFormatVersionLore {
		t.Errorf("a meta-only write moved the session to v%d; only a Stage write declares v5", s.Meta.FormatVersion)
	}
	_ = s.Close()
	if rows := stageRowsOf(t, path); len(rows) != 0 {
		t.Errorf("a meta-only write below v5 wrote %d stage rows", len(rows))
	}
	if _, got := openStage(t, path); !reflect.DeepEqual(got, fullStage()) {
		t.Errorf("after a note edit the legacy session loads Stage %+v, want %+v", got, fullStage())
	}
}

// The first Stage write migrates the session: v5, a stage row with the whole
// snapshot (not just the field that changed), and meta rows without Stage from
// then on.
func TestTheFirstStageWriteMigratesALegacySession(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := writeLegacyStageSession(t, dir, fullStage())
	s, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetBackground("bg_fog"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNote("after the migration"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	want := fullStage()
	want.Background = "bg_fog"
	_, got := openStage(t, path)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("migrated Stage = %+v, want %+v", got, want)
	}
	metas := metaRowsOf(t, path)
	if v := metas[len(metas)-1].FormatVersion; v != sessionFormatVersionStage {
		t.Errorf("the migrated session declares v%d, want v%d", v, sessionFormatVersionStage)
	}
	assertNoStageOnV5MetaRows(t, path)
}

// assertNoStageOnV5MetaRows checks the raw file: no meta row at v5 or later
// carries a Stage key.
func assertNoStageOnV5MetaRows(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stageKeys := stageJSONKeys(t)
	checked := 0
	for _, line := range strings.Split(string(raw), "\n") {
		var row struct {
			Type string                     `json:"type"`
			Meta map[string]json.RawMessage `json:"meta"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Type != "meta" {
			continue
		}
		var v int
		_ = json.Unmarshal(row.Meta["format_version"], &v)
		if v < sessionFormatVersionStage {
			continue
		}
		checked++
		for _, k := range stageKeys {
			if _, ok := row.Meta[k]; ok {
				t.Errorf("a v%d meta row carries Stage key %q: %s", v, k, line)
			}
		}
	}
	if checked == 0 {
		t.Error("no v5 meta row in the file, so nothing was checked")
	}
}

// stageJSONKeys lists the JSON names of Stage's fields, read from the type so a
// new field is covered.
func stageJSONKeys(t *testing.T) []string {
	t.Helper()
	var out []string
	st := reflect.TypeOf(Stage{})
	for i := range st.NumField() {
		name, _, _ := strings.Cut(st.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	if len(out) < 11 {
		t.Fatalf("Stage has %d JSON fields, want at least 11", len(out))
	}
	return out
}

func TestANewStageSessionWritesStageRowsAndCleanMetaRows(t *testing.T) {
	parent, _ := configuredStageSession(t)
	path := parent.Path
	want := parent.Stage
	_ = parent.Close()

	if len(stageRowsOf(t, path)) == 0 {
		t.Fatal("the Stage setters wrote no stage rows")
	}
	assertNoStageOnV5MetaRows(t, path)
	if _, got := openStage(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("reloaded Stage = %+v, want %+v", got, want)
	}
}

// A coding session never holds Stage state, so it never claims v5, even when
// a creation path calls SetCreationSpec with only a persona.
func TestACodingSessionStaysBelowV5(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "anthropic", "m", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	seed(t, s)
	if err := s.SetCreationSpec("kertoja", "", "", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBackground(""); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()
	if rows := stageRowsOf(t, path); len(rows) != 0 {
		t.Errorf("a coding session wrote %d stage rows", len(rows))
	}
	metas := metaRowsOf(t, path)
	last := metas[len(metas)-1]
	if last.FormatVersion >= sessionFormatVersionStage {
		t.Errorf("a coding session declares v%d", last.FormatVersion)
	}
	if last.Persona != "kertoja" {
		t.Errorf("persona = %q, want kertoja", last.Persona)
	}
}

// At v5 the persona has no stage row to ride, so SetCreationSpec writes a meta
// row for it as well.
func TestSetCreationSpecAtV5KeepsThePersona(t *testing.T) {
	s, _ := configuredStageSession(t)
	if err := s.SetCreationSpec("narrator", "chat", "card_other", nil, 0); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()
	re, got := openStage(t, path)
	if re.Meta.Persona != "narrator" || got.Card != "card_other" || got.Experience != "chat" {
		t.Errorf("after a v5 SetCreationSpec: persona %q card %q experience %q", re.Meta.Persona, got.Card, got.Experience)
	}
	if got.Background != "bg_rain" || got.UserName != "Kai" {
		t.Errorf("SetCreationSpec dropped the rest of the Stage: %+v", got)
	}
}

// Clearing everything at v5 writes an empty stage row, which folds as empty.
func TestClearingTheStageAtV5Persists(t *testing.T) {
	s, _ := configuredStageSession(t)
	s.writeMu.Lock()
	err := s.writeStageLocked(Stage{})
	s.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()
	if _, got := openStage(t, path); !got.IsZero() {
		t.Errorf("a cleared Stage reloaded as %+v", got)
	}
}

// Export and import round-trip meta rows through SessionMeta, so a legacy
// row's flat Stage fields must survive the struct, or every exported pre-v5
// Stage session imports as a coding session.
func TestALegacyStageSessionSurvivesExportAndImport(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := writeLegacyStageSession(t, dir, fullStage())
	exported, err := ExportSession(path, testsupport.TempDir(t))
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	imported, err := ImportSession(exported, testsupport.TempDir(t), "/elsewhere", "0.0.0-test")
	if err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	if _, got := openStage(t, imported); !reflect.DeepEqual(got, fullStage()) {
		t.Errorf("imported legacy Stage = %+v, want %+v", got, fullStage())
	}
}

func TestSessionMetaJSONKeepsLegacyStageFields(t *testing.T) {
	in := SessionMeta{ID: "x", FormatVersion: sessionFormatVersionLore, legacyStage: fullStage()}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionMeta
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
	// And a meta row with no legacy state marshals no Stage keys at all.
	b, _ = json.Marshal(SessionMeta{ID: "y", FormatVersion: sessionFormatVersionStage})
	for _, k := range stageJSONKeys(t) {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("a meta row with no Stage marshals %q: %s", k, b)
		}
	}
}

// The listing, the card-in-use scan, and the archive all read the summary, and
// archive then restore moves the file; each must see the stage rows.
func TestDescribeArchiveAndRestoreCarryTheStage(t *testing.T) {
	s, dir := configuredStageSession(t)
	path := s.Path
	id := SessionIDFromPath(path)
	_ = s.Close()

	sum := describeSession(path)
	if sum.Card != "card_kobeni" || sum.Experience != "play" || sum.Background != "bg_rain" || sum.World != "world_noir" {
		t.Errorf("describe = card %q experience %q background %q world %q", sum.Card, sum.Experience, sum.Background, sum.World)
	}
	if using := SessionsUsingCard(dir, "card_kobeni"); len(using) != 1 {
		t.Errorf("SessionsUsingCard found %d sessions, want 1", len(using))
	}

	archived, err := ArchiveSession(dir, dir, id)
	if err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if archived.Card != "card_kobeni" || archived.Background != "bg_rain" {
		t.Errorf("archived summary = card %q background %q", archived.Card, archived.Background)
	}
	restored, err := RestoreSession(dir, dir, id)
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	if _, got := openStage(t, restored); got.Card != "card_kobeni" || got.UserName != "Kai" || got.World != "world_noir" {
		t.Errorf("restored Stage = %+v", got)
	}
}

// A branch of a legacy session inherits its Stage as a stage row at v5.
func TestABranchOfALegacySessionCarriesTheStage(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := writeLegacyStageSession(t, dir, fullStage())
	child, err := BranchSession(path, dir, dir, "0.0.0-test", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	re, got := openStage(t, child)
	if !reflect.DeepEqual(got, fullStage()) {
		t.Errorf("branch Stage = %+v, want %+v", got, fullStage())
	}
	if re.Meta.FormatVersion != sessionFormatVersionStage {
		t.Errorf("branch declares v%d, want v%d", re.Meta.FormatVersion, sessionFormatVersionStage)
	}
	if rows := stageRowsOf(t, child); len(rows) != 1 {
		t.Errorf("branch wrote %d stage rows, want 1", len(rows))
	}
	assertNoStageOnV5MetaRows(t, child)
}
