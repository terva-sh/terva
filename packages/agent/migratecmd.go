package agent

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/i18n"
)

// runMigrateCommand dispatches `terva migrate ...`. Returns
// (handled=true, err) if rawArgs starts with "migrate"; otherwise
// (handled=false, nil) so the main router falls through to the regular
// flag parser. Mirrors the dispatch shape of runModelsCommand /
// runUpdateCommand so the router in cli.go stays uniform.
func runMigrateCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "migrate" {
		return false, nil
	}
	var opts migrateOptions
	for _, a := range rawArgs[1:] {
		switch a {
		case "-h", "--help", "help":
			printMigrateHelp()
			return true, nil
		case "-y", "--yes":
			opts.yes = true
		case "--remove-old":
			opts.removeOld = true
		case "--keep-old":
			opts.keepOld = true
		case "--dry-run":
			opts.dryRun = true
		default:
			printMigrateHelp()
			return true, i18n.Errorf("unknown flag for `migrate`: %s", a)
		}
	}
	if opts.removeOld && opts.keepOld {
		return true, fmt.Errorf("--remove-old and --keep-old are mutually exclusive")
	}
	return true, runMigrate(opts)
}

type migrateOptions struct {
	yes       bool // skip the copy / project-rename confirmations
	removeOld bool // delete the legacy dir without asking (implies consent to copy)
	keepOld   bool // never offer to delete the legacy dir
	dryRun    bool // describe the plan, change nothing
}

func printMigrateHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.migrate", `terva migrate — adopt the terva locations for data that zot left behind

usage:
  terva migrate                   interactive: copy the zot data dir, then ask
                                  about removing the original and renaming a
                                  project .zot/ dir
  terva migrate --dry-run          show what would be migrated; change nothing
  terva migrate --yes --keep-old   no prompts; copy everything, keep the old dir
  terva migrate --yes --remove-old no prompts; copy everything, delete the old dir
  terva migrate help               show this help

notes:
  * Copying never overwrites: a file that already exists at the
    destination is left alone and reported.
  * A successful run writes $TERVA_HOME/.zot-fallback-disabled, which
    stops terva from autoloading the legacy zot data dir and .zot/
    project dirs. Delete that file to re-enable the fallback.
  * ZOT_* environment variables keep working (with a deprecation
    warning), and .zotsession files import forever — migration changes
    neither.`))
}

func runMigrate(opts migrateOptions) error {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	plan := PlanMigration(cwd)

	// Plan summary, always shown.
	if plan.AlreadyMigrated {
		fmt.Println("already migrated: zot config autoloading is off")
	}
	if plan.UserDirApplicable() {
		fmt.Printf("data dir:    %s → %s\n", plan.OldDir, plan.NewDir)
	} else {
		fmt.Printf("data dir:    nothing to copy (using %s)\n", plan.NewDir)
	}
	switch {
	case plan.ProjectConflict:
		fmt.Printf("project dir: %s and %s both exist — merge them by hand, no rename offered\n", plan.ProjectOldDir, plan.ProjectNewDir)
	case plan.ProjectApplicable():
		fmt.Printf("project dir: %s → %s\n", plan.ProjectOldDir, plan.ProjectNewDir)
	}
	if plan.EnvNote != "" {
		fmt.Println("note:        " + plan.EnvNote)
	}

	if plan.NothingToDo() {
		fmt.Println("nothing to migrate")
		return nil
	}
	if opts.dryRun {
		return nil
	}

	in := bufio.NewReader(os.Stdin)
	assumeYes := opts.yes || opts.removeOld // --remove-old is meaningless without consent to copy

	// User data dir: copy, then maybe remove.
	copied := false
	var rep MigrationCopyReport
	if plan.UserDirApplicable() {
		if assumeYes || promptYesNo(in, fmt.Sprintf("copy %s → %s?", plan.OldDir, plan.NewDir), true) {
			rep = CopyUserData(plan.OldDir, plan.NewDir)
			copied = true
			fmt.Printf("copied %d file(s), %d symlink(s); skipped %d already-present\n",
				rep.FilesCopied, rep.SymlinksCopied, len(rep.SkippedExisting))
			for _, e := range rep.Errors {
				fmt.Fprintln(os.Stderr, "copy error:", e)
			}
		}
	}

	// The no-fallback marker: only after a clean copy (or when there
	// was nothing to copy in the first place).
	finalized := false
	if (!plan.UserDirApplicable() || (copied && rep.Clean())) && !plan.AlreadyMigrated {
		if err := FinalizeMigration(); err != nil {
			return fmt.Errorf("write the no-fallback marker: %w", err)
		}
		finalized = true
	}

	// Removal offer — never after a dirty or skipped copy.
	if copied && rep.Clean() && !opts.keepOld {
		if opts.removeOld || (!opts.yes && promptYesNo(in, fmt.Sprintf("remove the old zot dir %s?", plan.OldDir), false)) {
			if err := RemoveOldUserDir(plan, rep); err != nil {
				return err
			}
			fmt.Println("removed", plan.OldDir)
		}
	} else if copied && !rep.Clean() {
		fmt.Fprintf(os.Stderr, "old dir kept: %d copy error(s) above\n", len(rep.Errors))
	}

	// Project dir rename, its own confirmation.
	if plan.ProjectApplicable() {
		if assumeYes || promptYesNo(in, fmt.Sprintf("rename %s → %s?", plan.ProjectOldDir, plan.ProjectNewDir), true) {
			if err := RenameProjectDir(plan); err != nil {
				return err
			}
			fmt.Printf("renamed %s → %s\n", plan.ProjectOldDir, plan.ProjectNewDir)
		}
	}

	if finalized {
		fmt.Printf("zot autoloading disabled: terva now reads only %s and .terva/ project dirs\n", plan.NewDir)
		fmt.Println("(ZOT_* environment variables still work but are deprecated; .zotsession import is unaffected)")
	}
	if plan.EnvNote != "" {
		fmt.Println("reminder: " + plan.EnvNote)
	}
	return nil
}

// promptYesNo asks a y/n question on stderr and reads the answer from
// in. An empty answer takes the default; anything unrecognized is no.
func promptYesNo(in *bufio.Reader, question string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(os.Stderr, "%s %s ", question, hint)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return false // closed stdin: never assume consent
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}
