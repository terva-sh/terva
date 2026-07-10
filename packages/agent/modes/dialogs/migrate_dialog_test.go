package dialogs

import (
	"errors"
	"testing"

	"terva.sh/terva/packages/tui"
)

func key(k tui.KeyKind) tui.Key { return tui.Key{Kind: k} }

func userDirState() MigrationState {
	return MigrationState{
		OldDir:            "/old/zot",
		NewDir:            "/new/terva",
		UserDirApplicable: true,
	}
}

func TestMigrateDialogCopyThenKeep(t *testing.T) {
	d := NewMigrateDialog()
	d.Open(userDirState())
	if d.stage != migrateStageConfirmCopy {
		t.Fatalf("stage = %v, want ConfirmCopy", d.stage)
	}

	act := d.HandleKey(key(tui.KeyEnter)) // cursor 0 = start migration
	if !act.StartCopy || !d.Loading() {
		t.Fatalf("enter on start: act=%+v loading=%v", act, d.Loading())
	}
	// Keys are ignored mid-copy.
	if act := d.HandleKey(key(tui.KeyEsc)); act != (migrateAction{}) || !d.Loading() {
		t.Fatalf("copying must ignore keys, got %+v", act)
	}

	d.SetCopyResult(MigrationCopyResult{FilesCopied: 3, Clean: true}, nil)
	if d.stage != migrateStageRemove {
		t.Fatalf("clean copy with no project dir must reach Remove, got %v", d.stage)
	}
	if d.cursor != 0 {
		t.Fatal("Remove stage must default the cursor to keep")
	}

	act = d.HandleKey(key(tui.KeyEnter)) // keep
	if !act.KeepOld || act.RemoveAndExit {
		t.Fatalf("enter on keep: %+v", act)
	}
	if d.stage != migrateStageDone {
		t.Fatalf("stage = %v, want Done", d.stage)
	}
	if act := d.HandleKey(key(tui.KeyEnter)); !act.Close || d.Active() {
		t.Fatalf("Done close: %+v active=%v", act, d.Active())
	}
}

func TestMigrateDialogRemovePath(t *testing.T) {
	d := NewMigrateDialog()
	d.Open(userDirState())
	d.HandleKey(key(tui.KeyEnter))
	d.SetCopyResult(MigrationCopyResult{FilesCopied: 1, Clean: true}, nil)

	d.HandleKey(key(tui.KeyDown)) // move to the remove row
	act := d.HandleKey(key(tui.KeyEnter))
	if !act.RemoveAndExit {
		t.Fatalf("enter on remove row: %+v", act)
	}
}

func TestMigrateDialogEscNeverRemoves(t *testing.T) {
	d := NewMigrateDialog()
	d.Open(userDirState())
	d.HandleKey(key(tui.KeyEnter))
	d.SetCopyResult(MigrationCopyResult{Clean: true}, nil)
	d.HandleKey(key(tui.KeyDown)) // even with the remove row selected
	act := d.HandleKey(key(tui.KeyEsc))
	if act.RemoveAndExit || !act.KeepOld {
		t.Fatalf("esc at Remove: %+v", act)
	}
	if d.stage != migrateStageDone {
		t.Fatalf("stage = %v, want Done", d.stage)
	}
}

func TestMigrateDialogDirtyCopySkipsRemove(t *testing.T) {
	d := NewMigrateDialog()
	d.Open(userDirState())
	d.HandleKey(key(tui.KeyEnter))
	d.SetCopyResult(MigrationCopyResult{FilesCopied: 1, Errors: []string{"boom"}, Clean: false}, nil)
	if d.stage != migrateStageDone {
		t.Fatalf("dirty copy must skip Remove, got %v", d.stage)
	}

	d = NewMigrateDialog()
	d.Open(userDirState())
	d.HandleKey(key(tui.KeyEnter))
	d.SetCopyResult(MigrationCopyResult{}, errors.New("copy exploded"))
	if d.stage != migrateStageDone || d.copyErr == "" {
		t.Fatalf("copy error must land on Done with the message, got %v %q", d.stage, d.copyErr)
	}
}

func TestMigrateDialogProjectFlow(t *testing.T) {
	st := userDirState()
	st.ProjectOldDir = "/repo/.zot"
	st.ProjectNewDir = "/repo/.terva"
	st.ProjectApplicable = true

	d := NewMigrateDialog()
	d.Open(st)
	d.HandleKey(key(tui.KeyEnter))
	d.SetCopyResult(MigrationCopyResult{Clean: true}, nil)
	if d.stage != migrateStageProject {
		t.Fatalf("project stage must come before Remove, got %v", d.stage)
	}
	act := d.HandleKey(key(tui.KeyEnter)) // cursor 0 = rename
	if !act.RenameProject {
		t.Fatalf("enter on rename: %+v", act)
	}
	d.SetRenameResult(nil)
	if d.stage != migrateStageRemove || !d.renamed {
		t.Fatalf("after rename: stage=%v renamed=%v", d.stage, d.renamed)
	}

	// Project-only opening (nothing to copy).
	st2 := MigrationState{ProjectOldDir: "/repo/.zot", ProjectNewDir: "/repo/.terva", ProjectApplicable: true}
	d2 := NewMigrateDialog()
	d2.Open(st2)
	if d2.stage != migrateStageProject {
		t.Fatalf("project-only open: stage = %v", d2.stage)
	}
	// Skipping the rename with no copy run lands on Done, never Remove.
	d2.HandleKey(key(tui.KeyDown))
	if act := d2.HandleKey(key(tui.KeyEnter)); act.RenameProject || d2.stage != migrateStageDone {
		t.Fatalf("skip on project-only: act=%+v stage=%v", act, d2.stage)
	}
}
