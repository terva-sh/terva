package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
)

// builtinRoster returns every shipped persona, as the library shows them.
//
// Enrolled by scanning rather than listed, because a list is what let this
// stay broken: every persona test in this package used Mieli, one of the six
// top-level built-ins, and the copy-to-edit contract held for exactly those. It
// failed for the other ten and no test could see it. A crew member added
// tomorrow, in a new team directory or with a filename that is not its name, is
// covered here the moment it ships.
func builtinRoster(t *testing.T, w *Workspace) []ctrlproto.PersonaSummary {
	t.Helper()
	r, err := w.PersonasList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []ctrlproto.PersonaSummary
	for _, p := range r.Personas {
		if p.Origin == "built-in" {
			out = append(out, p)
		}
	}
	// Vacuity floor. An empty roster passes every per-persona assertion below.
	if len(out) < 10 {
		t.Fatalf("only %d built-in personas in the roster; the scan is broken and a pass proves nothing", len(out))
	}
	return out
}

// 🔑 The copy-to-edit contract, for EVERY shipped persona rather than the one
// it happened to hold for.
//
// Editing a built-in writes a user file that shadows it. "Shadows" is a precise
// claim with three parts, and the old write path satisfied none of them for a
// namespaced persona: the ref keeps resolving to YOUR copy, the roster carries
// ONE persona of that ref rather than two, and the built-in is gone from the
// merged view until you delete the copy.
func TestEveryBuiltinCopyToEditsIntoItsOwnRef(t *testing.T) {
	for _, b := range builtinRoster(t, newPersonaWorkspace(t)) {
		t.Run(b.Ref, func(t *testing.T) {
			w := newPersonaWorkspace(t)
			ctx := context.Background()
			if err := config.TrustPath(w.cwd, false); err != nil {
				t.Fatal(err)
			}

			edited, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{
				Name: b.Name, Summary: "mine", Charter: "My own " + b.Name + ".",
			}))
			if err != nil {
				t.Fatalf("edit %s: %v", b.Ref, err)
			}
			if edited.Ref != b.Ref {
				t.Errorf("the copy took ref %q, but the persona it copied is %q.\n"+
					"  Nothing resolves the copy: --persona %s, a swarm dispatch and the roster all still "+
					"reach the built-in, while this call reported the edit as saved.", edited.Ref, b.Ref, b.Ref)
			}
			if edited.Origin != "user" || !edited.Editable {
				t.Errorf("copy-to-edit should yield an editable user persona: %+v", edited.PersonaSummary)
			}

			// The ref resolves to the copy — this is the claim that matters, and
			// the one a same-named neighbour cannot satisfy.
			got, err := persona.LoadByName(b.Ref)
			if err != nil {
				t.Fatalf("LoadByName(%s): %v", b.Ref, err)
			}
			if got.Summary != "mine" {
				t.Errorf("%s still resolves to the built-in (%q) after being edited", b.Ref, got.Summary)
			}

			// And exactly one persona answers to it. Two is the shape of the bug:
			// the roster grew a duplicate that shadowed nothing.
			r, err := w.PersonasList(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var byName []string
			for _, p := range r.Personas {
				if strings.EqualFold(p.Name, b.Name) {
					byName = append(byName, p.Ref+"("+p.Origin+")")
				}
			}
			if len(byName) != 1 {
				t.Errorf("the roster carries %d personas named %q after one edit: %v", len(byName), b.Name, byName)
			}
		})
	}
}

// The way back, for every shipped persona: deleting the copy un-shadows the
// built-in. Paired with the test above deliberately — a write that lands on the
// wrong file leaves a delete that cannot find it, and the pair is what proves
// they agree on which file a persona occupies.
func TestEveryBuiltinUnShadowsWhenTheCopyIsDeleted(t *testing.T) {
	for _, b := range builtinRoster(t, newPersonaWorkspace(t)) {
		t.Run(b.Ref, func(t *testing.T) {
			w := newPersonaWorkspace(t)
			ctx := context.Background()
			if err := config.TrustPath(w.cwd, false); err != nil {
				t.Fatal(err)
			}
			if _, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{
				Name: b.Name, Summary: "mine", Charter: "Mine.",
			})); err != nil {
				t.Fatalf("edit %s: %v", b.Ref, err)
			}
			// By ref — what the library sends (Library.tsx passes p.ref).
			if err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: b.Ref}); err != nil {
				t.Fatalf("delete %s: %v", b.Ref, err)
			}
			restored, err := persona.LoadByName(b.Ref)
			if err != nil {
				t.Fatalf("LoadByName(%s) after delete: %v", b.Ref, err)
			}
			if !restored.Builtin() {
				t.Errorf("%s resolves to %q after the copy was deleted, want the built-in back", b.Ref, restored.Source)
			}
		})
	}
}

// `terva persona init` is the CLI's copy-to-edit: it materializes the crew at
// their own paths, team directories and all. Those files are the user's, and
// the library has to treat them as such — this is the flow that produced
// "persona X is user, not yours to delete", a refusal that named your own file
// as the reason you could not remove it.
func TestAPersonaInitCopyIsEditableAndDeletable(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}

	// What personaInit writes: the built-in's bytes at the built-in's rel path.
	src, err := persona.LoadByName("review-crew:vartija")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := persona.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	initCopy := filepath.Join(persona.Dir(), "review-crew", "vartija.md")
	if err := os.MkdirAll(filepath.Dir(initCopy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initCopy, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// An edit lands on THAT file, not beside it.
	if _, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{
		Name: "Vartija", Summary: "hand-edited", Charter: "Edited.",
	})); err != nil {
		t.Fatalf("edit: %v", err)
	}
	after, err := os.ReadFile(initCopy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "hand-edited") {
		t.Errorf("the edit did not reach %s — it says:\n%s", initCopy, after)
	}
	if _, err := os.Stat(filepath.Join(persona.Dir(), "vartija.md")); err == nil {
		t.Error("the edit minted a second, top-level vartija.md beside the file it was editing")
	}

	// And it can be removed, by ref or by name.
	if err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: "review-crew:vartija"}); err != nil {
		t.Fatalf("delete by ref: %v", err)
	}
	if _, err := os.Stat(initCopy); err == nil {
		t.Error("the file survived its own delete")
	}
}

// 🪤 A persona the roster reports as YOURS must be deletable, by every name the
// roster gives you for it.
//
// This is the shape of the bug, and it is the shape the first draft of this
// test missed: exercising only built-ins, every refusal correctly said
// "built-in" and the test passed against the unfixed code. What produced
// "persona X is user, not yours to delete" was a file of the user's that Delete
// could not find — so the file has to be planted, and each of these cases
// failed before the store learned about namespaces.
func TestDeletingYourOwnPersonaIsNeverRefusedAsNotYours(t *testing.T) {
	cases := []struct {
		what     string
		rel      string // where the file sits under $TERVA_HOME/personas
		deleteBy string // the name or ref the library sends
	}{
		{"top-level, by name", "vartija", "Vartija"},
		{"team directory, by ref", "review-crew/vartija", "review-crew:vartija"},
		{"team directory, by name", "review-crew/vartija", "Vartija"},
		// Filed under a stem that is not its name, which is how the crew that
		// gets convened by ref is shipped.
		{"stem unlike its name, by ref", "raati-crew/yata", "raati-crew:yata"},
		{"stem unlike its name, by name", "raati-crew/yata", "YATA-1"},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			w := newPersonaWorkspace(t)
			ctx := context.Background()
			if err := config.TrustPath(w.cwd, false); err != nil {
				t.Fatal(err)
			}
			name := "Vartija"
			if strings.Contains(tc.rel, "yata") {
				name = "YATA-1"
			}
			file := filepath.Join(persona.Dir(), filepath.FromSlash(tc.rel)+".md")
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("---\nname: "+name+"\nsummary: mine\n---\n\nMine.\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: tc.deleteBy}); err != nil {
				t.Fatalf("deleting %s (your own file at %s) was refused: %v", tc.deleteBy, file, err)
			}
			if _, err := os.Stat(file); err == nil {
				t.Errorf("%s reported deleted but %s is still there", tc.deleteBy, file)
			}
		})
	}
}

// plantUserPersona writes a user persona at rel ("review-crew/vartija"), the
// way a hand-authored library or `terva persona init` leaves one.
func plantUserPersona(t *testing.T, rel, name, summary string) string {
	t.Helper()
	dest := filepath.Join(persona.Dir(), filepath.FromSlash(rel)+".md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\nsummary: " + summary + "\n---\n\nCharter of " + summary + ".\n"
	if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dest
}

// 🔑 A write targets the persona its REF names, not the one its name resolves
// to. Those are different personas exactly when two share a name — which is the
// only case that can distinguish them, and the reason the ref exists.
//
// The editor opens a persona by ref (personas.get) and used to save it by name.
// Open the namespaced one of two Vartija and press save, and the write landed on
// the other one: the persona you were looking at was untouched and a persona you
// had not opened was overwritten, with a success reported for both.
func TestAnEditTargetsTheRefEvenWhenTheNameResolvesElsewhere(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	flat := plantUserPersona(t, "vartija", "Vartija", "FLAT")
	nested := plantUserPersona(t, "review-crew/vartija", "Vartija", "NESTED")

	saved, err := w.PersonasEdit(ctx, ctrlproto.PersonaEditParams{
		Ref: "review-crew:vartija",
		PersonaWriteParams: ctrlproto.PersonaWriteParams{
			Name: "Vartija", Summary: "EDITED", Charter: "Edited.",
		},
	})
	if err != nil {
		t.Fatalf("edit by ref: %v", err)
	}

	got, err := os.ReadFile(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "EDITED") {
		t.Errorf("the edit did not reach the persona its ref named (%s):\n%s", nested, got)
	}
	untouched, err := os.ReadFile(flat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(untouched), "FLAT") {
		t.Errorf("the edit overwrote %s — a persona the caller never opened:\n%s", flat, untouched)
	}

	// And the view it answers with is the persona it wrote. Re-reading by name
	// would describe the top-level Vartija, which this call did not touch.
	if saved.Ref != "review-crew:vartija" || saved.Summary != "EDITED" {
		t.Errorf("the write reported ref=%q summary=%q, but it wrote %s",
			saved.Ref, saved.Summary, nested)
	}
}

// A client that sends no ref identifies by name, exactly as it did before the
// field existed. Held so the fallback is a supported path rather than an
// accident nobody would notice breaking.
func TestAnEditWithNoRefStillIdentifiesByName(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	nested := plantUserPersona(t, "review-crew/vartija", "Vartija", "NESTED")

	if _, err := w.PersonasEdit(ctx, editParams(ctrlproto.PersonaWriteParams{
		Name: "Vartija", Summary: "EDITED", Charter: "Edited.",
	})); err != nil {
		t.Fatalf("edit by name: %v", err)
	}
	got, err := os.ReadFile(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "EDITED") {
		t.Errorf("a ref-less edit did not reach %s:\n%s", nested, got)
	}
}

// The refusals that ARE correct stay correct: a built-in is not yours, by
// either of the names the roster prints for it. Held separately from the case
// above so neither can be mistaken for evidence of the other.
func TestDeletingABuiltinIsStillRefusedByRefAndByName(t *testing.T) {
	w := newPersonaWorkspace(t)
	ctx := context.Background()
	if err := config.TrustPath(w.cwd, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"review-crew:vartija", "Vartija", "raati-crew:yata", "YATA-1"} {
		err := w.PersonasDelete(ctx, ctrlproto.PersonaDeleteParams{Name: name})
		if err == nil {
			t.Errorf("deleting the built-in %q should be refused", name)
			continue
		}
		if !strings.Contains(err.Error(), "built-in") {
			t.Errorf("the refusal for %q should say it is a built-in: %v", name, err)
		}
	}
}
