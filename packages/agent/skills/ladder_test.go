package skills

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// mkSkill writes a minimal SKILL.md under dir/<name>/, with extra frontmatter
// lines spliced in verbatim.
func mkSkill(t *testing.T, dir, name, desc string, extra ...string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	front := "---\nname: " + name + "\ndescription: " + desc + "\n"
	for _, e := range extra {
		front += e + "\n"
	}
	front += "---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(front), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkExtension writes an enabled extension with a bundled skill.
func mkExtension(t *testing.T, root, extName, skillName, desc string) {
	t.Helper()
	extDir := filepath.Join(root, extName)
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), []byte(`{"name":"`+extName+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mkSkill(t, filepath.Join(extDir, "skills"), skillName, desc)
}

// anyBuiltinName returns the name of a skill that actually ships in the
// binary, so the shadowing tests aim at a real collision rather than a
// hypothetical one.
func anyBuiltinName(t *testing.T) string {
	t.Helper()
	b := loadBuiltins()
	if len(b) == 0 {
		t.Fatal("no built-in skills embedded — the shadowing tests would pass vacuously")
	}
	return b[0].Name
}

// The reported bug: a ~/.claude/skills/<name> written for another runtime took
// the bare name away from the built-in terva ships and documents.
//
// The "must still succeed" half is not optional here. A reorder that simply
// pinned built-ins to the top would pass the first assertion and silently
// break deliberate native overrides, so both directions are checked against
// the same built-in name.
func TestBuiltinOutranksCompatDirsButNotNativeDirs(t *testing.T) {
	name := anyBuiltinName(t)

	t.Run("claude compat does NOT shadow a built-in", func(t *testing.T) {
		tmp := testsupport.TempDir(t)
		tervaHome := filepath.Join(tmp, "home")
		cwd := filepath.Join(tmp, "proj")
		userHome := filepath.Join(tmp, "user")
		mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "the foreign one")
		mkSkill(t, filepath.Join(cwd, ".claude", "skills"), name, "the foreign project one")

		got, _ := Discover(tervaHome, cwd, userHome, true, true, Gate{TrustProject: true})
		s := FindByName(got, name)
		if s == nil {
			t.Fatalf("%q vanished entirely", name)
		}
		if !s.Builtin {
			t.Fatalf("%q resolved to %s; the built-in must win over a .claude skill", name, s.Source)
		}
	})

	t.Run("agents compat does NOT shadow a built-in", func(t *testing.T) {
		tmp := testsupport.TempDir(t)
		userHome := filepath.Join(tmp, "user")
		mkSkill(t, filepath.Join(userHome, ".agents", "skills"), name, "the agents one")

		got, _ := Discover(filepath.Join(tmp, "home"), "", userHome, true, true, Gate{TrustProject: true})
		if s := FindByName(got, name); s == nil || !s.Builtin {
			t.Fatalf("built-in %q lost its name to a .agents skill: %+v", name, s)
		}
	})

	t.Run("an extension bundle does NOT shadow a built-in", func(t *testing.T) {
		tmp := testsupport.TempDir(t)
		tervaHome := filepath.Join(tmp, "home")
		mkExtension(t, filepath.Join(tervaHome, "extensions"), "widgets", name, "from a bundle")

		got, _ := Discover(tervaHome, "", "", true, true, Gate{TrustProject: true})
		if s := FindByName(got, name); s == nil || !s.Builtin {
			t.Fatalf("built-in %q lost its name to an extension bundle: %+v", name, s)
		}
	})

	t.Run("a native skill DOES shadow a built-in", func(t *testing.T) {
		tmp := testsupport.TempDir(t)
		tervaHome := filepath.Join(tmp, "home")
		mkSkill(t, filepath.Join(tervaHome, "skills"), name, "deliberately mine")

		got, _ := Discover(tervaHome, "", "", true, true, Gate{TrustProject: true})
		s := FindByName(got, name)
		if s == nil {
			t.Fatalf("%q vanished entirely", name)
		}
		if s.Builtin {
			t.Fatalf("a $TERVA_HOME/skills/%s must override the built-in, but the built-in won", name)
		}
		if s.Description != "deliberately mine" {
			t.Fatalf("resolved to the wrong skill: %q", s.Description)
		}
	})

	t.Run("--no-builtin-skills hands the name back down the ladder", func(t *testing.T) {
		tmp := testsupport.TempDir(t)
		userHome := filepath.Join(tmp, "user")
		mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "the foreign one")

		got, _ := Discover(filepath.Join(tmp, "home"), "", userHome, true, false /* includeBuiltin */, Gate{TrustProject: true})
		s := FindByName(got, name)
		if s == nil || s.Description != "the foreign one" {
			t.Fatalf("with built-ins dropped the .claude skill should take the name, got %+v", s)
		}
	})
}

// An extension bundle still must not shadow a skill the user wrote — the rung
// order below the built-ins is unchanged.
func TestExtensionBundleStillLosesToUserSkills(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	mkSkill(t, filepath.Join(tervaHome, "skills"), "shared", "mine")
	mkExtension(t, filepath.Join(tervaHome, "extensions"), "widgets", "shared", "the bundle's")

	got, _ := Discover(tervaHome, "", "", true, true, Gate{TrustProject: true})
	if s := FindByName(got, "shared"); s == nil || s.Description != "mine" {
		t.Fatalf("a bundle shadowed a deliberately-written skill: %+v", s)
	}
}
