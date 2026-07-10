package dialogs

import (
	"fmt"

	"terva.sh/terva/packages/tui"
)

// MigrationState mirrors agent.MigrationPlan for the /migrate dialog.
// modes can't import agent (agent imports modes), so the host maps the
// plan into this struct via MigrationHooks.Plan — same pattern as
// UpdateInfo / ChangelogPayload.
type MigrationState struct {
	OldDir, NewDir               string
	EnvNote                      string
	ProjectOldDir, ProjectNewDir string
	ProjectConflict              bool
	UserDirApplicable            bool
	ProjectApplicable            bool
	AlreadyMigrated              bool
	NothingToDo                  bool
}

// MigrationCopyResult mirrors agent.MigrationCopyReport.
type MigrationCopyResult struct {
	FilesCopied     int
	SymlinksCopied  int
	SkippedExisting int
	Errors          []string
	Clean           bool
}

// MigrationHooks is how the TUI reaches the migration engine. All
// four are required for /migrate to be offered; a nil Migration
// config disables the command with a status-line error.
type MigrationHooks struct {
	// Plan computes a fresh migration plan for the session's cwd.
	Plan func() MigrationState
	// CopyUserData copies the legacy dir into the new one. The host
	// also writes the no-fallback marker after a clean copy.
	CopyUserData func() (MigrationCopyResult, error)
	// Finalize writes the no-fallback marker for runs with no
	// user-dir step (project-only migrations).
	Finalize func() error
	// RemoveOldDir deletes the legacy dir; only called after a clean
	// copy, from the explicit "remove and exit" choice.
	RemoveOldDir func() error
	// RenameProject renames the project's .zot/ to .terva/.
	RenameProject func() error
}

// migrateStage is where the staged /migrate dialog currently is.
// Stages run ConfirmCopy → Copying → Project → Remove → Done, with
// non-applicable stages skipped. Remove is deliberately LAST: choosing
// it exits the TUI, so everything else must already be settled.
type migrateStage int

const (
	migrateStageConfirmCopy migrateStage = iota
	migrateStageCopying
	migrateStageProject
	migrateStageRemove
	migrateStageDone
)

// migrateAction is the outcome of a key press, consumed by the
// interactive key loop.
type migrateAction struct {
	StartCopy     bool // run MigrationHooks.CopyUserData on a goroutine
	RenameProject bool // run MigrationHooks.RenameProject (sync, fast)
	RemoveAndExit bool // run MigrationHooks.RemoveOldDir; on success the TUI exits
	KeepOld       bool // user kept the legacy dir; surface the restart warning
	Close         bool
}

type MigrateDialog struct {
	active bool
	stage  migrateStage
	cursor int
	state  MigrationState

	copyRan bool
	result  MigrationCopyResult
	copyErr string

	renamed   bool
	renameErr string
	keptOld   bool
}

func NewMigrateDialog() *MigrateDialog { return &MigrateDialog{} }

// Open starts the dialog at the first applicable stage.
func (d *MigrateDialog) Open(st MigrationState) {
	*d = MigrateDialog{active: true, state: st}
	switch {
	case st.UserDirApplicable:
		d.stage = migrateStageConfirmCopy
	case st.ProjectApplicable:
		d.stage = migrateStageProject
	default:
		d.stage = migrateStageDone
	}
}

func (d *MigrateDialog) Close()       { d.active = false }
func (d *MigrateDialog) Active() bool { return d != nil && d.active }

// Loading reports whether the background copy is in flight; keys are
// ignored and the tick loop keeps redrawing while it is.
func (d *MigrateDialog) Loading() bool { return d.Active() && d.stage == migrateStageCopying }

// options returns the selectable rows for the current stage.
func (d *MigrateDialog) options() []string {
	switch d.stage {
	case migrateStageConfirmCopy:
		return []string{"start migration", "cancel"}
	case migrateStageProject:
		return []string{
			fmt.Sprintf("rename %s → %s", d.state.ProjectOldDir, d.state.ProjectNewDir),
			"skip — keep the .zot/ name",
		}
	case migrateStageRemove:
		return []string{
			"keep the old dir — continue this session (restart terva later)",
			fmt.Sprintf("remove %s and exit terva (restart required)", d.state.OldDir),
		}
	case migrateStageDone:
		return []string{"close"}
	}
	return nil
}

// HandleKey advances the selection or resolves the current stage.
func (d *MigrateDialog) HandleKey(k tui.Key) migrateAction {
	if d.stage == migrateStageCopying {
		return migrateAction{} // not cancellable: a half-copied dir helps nobody
	}
	opts := d.options()
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(opts)-1 {
			d.cursor++
		}
	case tui.KeyEsc:
		// Esc backs out of the current stage without doing it; it can
		// never remove anything.
		switch d.stage {
		case migrateStageConfirmCopy:
			d.Close()
			return migrateAction{Close: true}
		case migrateStageProject:
			d.advanceFromProject()
		case migrateStageRemove:
			d.keptOld = true
			d.toDone()
			return migrateAction{KeepOld: true}
		default:
			d.Close()
			return migrateAction{Close: true}
		}
	case tui.KeyEnter:
		switch d.stage {
		case migrateStageConfirmCopy:
			if d.cursor == 0 {
				d.stage = migrateStageCopying
				d.cursor = 0
				return migrateAction{StartCopy: true}
			}
			d.Close()
			return migrateAction{Close: true}
		case migrateStageProject:
			if d.cursor == 0 {
				return migrateAction{RenameProject: true}
			}
			d.advanceFromProject()
		case migrateStageRemove:
			if d.cursor == 1 {
				return migrateAction{RemoveAndExit: true}
			}
			d.keptOld = true
			d.toDone()
			return migrateAction{KeepOld: true}
		case migrateStageDone:
			d.Close()
			return migrateAction{Close: true}
		}
	}
	return migrateAction{}
}

// SetCopyResult delivers the background copy outcome and advances the
// stage machine.
func (d *MigrateDialog) SetCopyResult(res MigrationCopyResult, err error) {
	if !d.Active() || d.stage != migrateStageCopying {
		return
	}
	d.copyRan = true
	d.result = res
	if err != nil {
		d.copyErr = err.Error()
	}
	if d.state.ProjectApplicable {
		d.stage = migrateStageProject
		d.cursor = 0
		return
	}
	d.afterProject()
}

// SetRenameResult records the project rename outcome and advances.
func (d *MigrateDialog) SetRenameResult(err error) {
	d.renamed = err == nil
	if err != nil {
		d.renameErr = err.Error()
	}
	d.afterProject()
}

// advanceFromProject is the "skip" path out of the project stage.
func (d *MigrateDialog) advanceFromProject() { d.afterProject() }

// afterProject decides between the removal offer and the summary: the
// old dir may only be offered for removal after a clean, completed
// copy.
func (d *MigrateDialog) afterProject() {
	if d.copyRan && d.copyErr == "" && d.result.Clean {
		d.stage = migrateStageRemove
		d.cursor = 0 // default to "keep" — the destructive row is never pre-selected
		return
	}
	d.toDone()
}

func (d *MigrateDialog) toDone() {
	d.stage = migrateStageDone
	d.cursor = 0
}

// Render returns the dialog lines.
func (d *MigrateDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	lines := []string{FrameHeader(th, "migrate from zot", width)}
	body := func(s string) { lines = append(lines, th.FG256(th.Muted, s)) }

	switch d.stage {
	case migrateStageConfirmCopy:
		body(fmt.Sprintf("copy %s → %s", d.state.OldDir, d.state.NewDir))
		body("existing files at the destination are never overwritten")
		if d.state.EnvNote != "" {
			body("note: " + d.state.EnvNote)
		}
	case migrateStageCopying:
		body(fmt.Sprintf("copying %s → %s …", d.state.OldDir, d.state.NewDir))
	case migrateStageProject:
		if d.copyRan {
			body(d.copySummary())
		}
		body(fmt.Sprintf("this project has a legacy %s directory", d.state.ProjectOldDir))
	case migrateStageRemove:
		body(d.copySummary())
		if d.renameMsg() != "" {
			body(d.renameMsg())
		}
		body("the old dir is now redundant — removing it requires exiting terva")
		body("(this session would otherwise keep writing into the deleted dir)")
	case migrateStageDone:
		switch {
		case d.state.NothingToDo:
			body("nothing to migrate — already on the terva locations")
		case !d.copyRan && !d.state.UserDirApplicable && d.state.ProjectConflict:
			body(fmt.Sprintf("%s and %s both exist — merge them by hand", d.state.ProjectOldDir, d.state.ProjectNewDir))
		default:
			if d.copyRan {
				body(d.copySummary())
			}
			if d.renameMsg() != "" {
				body(d.renameMsg())
			}
			if d.copyErr != "" {
				body("copy failed: " + d.copyErr)
			}
			for _, e := range d.result.Errors {
				body("copy error: " + e)
			}
			if d.keptOld {
				body(fmt.Sprintf("old dir kept — anything written before you restart stays in %s", d.state.OldDir))
				body("and will be ignored afterward; restart terva when convenient")
			}
		}
		if d.state.EnvNote != "" {
			body("note: " + d.state.EnvNote)
		}
	}

	lines = append(lines, "")
	hint := "↑/↓ to choose, enter to confirm, esc to back out"
	if d.stage == migrateStageCopying {
		hint = "copying — hang tight"
	}
	body(hint)
	for idx, opt := range d.options() {
		plain := "  " + opt
		if idx == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

func (d *MigrateDialog) copySummary() string {
	if d.copyErr != "" {
		return "copy failed: " + d.copyErr
	}
	s := fmt.Sprintf("copied %d file(s), %d symlink(s); skipped %d already-present",
		d.result.FilesCopied, d.result.SymlinksCopied, d.result.SkippedExisting)
	if n := len(d.result.Errors); n > 0 {
		s += fmt.Sprintf("; %d error(s)", n)
	}
	return s
}

func (d *MigrateDialog) renameMsg() string {
	switch {
	case d.renameErr != "":
		return "project rename failed: " + d.renameErr
	case d.renamed:
		return fmt.Sprintf("renamed %s → %s", d.state.ProjectOldDir, d.state.ProjectNewDir)
	}
	return ""
}
