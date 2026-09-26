package talkoot

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// rawLines returns the room file's lines as written.
func (f *fixture) rawLines() [][]byte {
	f.t.Helper()
	b, err := os.ReadFile(OpenRoom(f.dir).Path())
	if err != nil {
		f.t.Fatal(err)
	}
	return bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
}

func (f *fixture) writeRaw(lines [][]byte) {
	f.t.Helper()
	b := append(bytes.Join(lines, []byte("\n")), '\n')
	if err := os.WriteFile(OpenRoom(f.dir).Path(), b, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) damagedReasons() []string {
	var out []string
	for _, l := range f.lines() {
		if l.Type == LineDamaged {
			out = append(out, l.Reason)
		}
	}
	return out
}

// 🚨 A line the router did not write fails its seal. Here a member forges a
// resume that would lift its own spend cap.
func TestAForgedLinePausesTheTalkoot(t *testing.T) {
	// The key id is in every line, so a forger copies it. Only the MAC is
	// out of reach.
	for name, forged := range map[string]string{
		"no seal":   `{"type":"resume","member":"jev","by":"human:sothr","kid":"KID"}`,
		"false MAC": `{"type":"resume","member":"jev","by":"human:sothr","kid":"KID","mac":"` + strings.Repeat("0", 64) + `"}`,
	} {
		f := newFixture(t, nil)
		if err := f.router.TurnEnded("jev", 20); err != nil {
			t.Fatal(err)
		}
		k, _, err := loadKey(f.dir)
		if err != nil {
			t.Fatal(err)
		}
		f.writeRaw(append(f.rawLines(), []byte(strings.Replace(forged, "KID", k.id, 1))))
		f.reopen()
		why := f.pausedWhy("jev")
		if !strings.Contains(why, "$20.00") {
			t.Errorf("%s: the forged resume must not lift the cap, got %q", name, why)
		}
		if !strings.Contains(why, "line 3 of the room") {
			t.Errorf("%s: the forged line must pause the talkoot, got %q", name, why)
		}
	}
}

// An edited line fails, and so does every line after it: each seals to the
// line before it, and nothing after a tampered line is trusted. A person's
// resume chains on from the last line that verified.
func TestAnEditedLineFailsEverythingAfterIt(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnEnded("jev", 5); err != nil {
		t.Fatal(err)
	}
	if err := f.router.TurnEnded("gage", 1); err != nil {
		t.Fatal(err)
	}
	lines := f.rawLines()
	lines[0] = bytes.Replace(lines[0], []byte(`"cost_usd":5`), []byte(`"cost_usd":0.5`), 1)
	f.writeRaw(lines)
	got := f.damagedReasons()
	if len(got) != 3 || !strings.Contains(got[0], "line 1 of the room fails its seal") ||
		!strings.Contains(got[1], "line 2 of the room fails its seal") || !strings.Contains(got[2], "2 of the 2 lines") {
		t.Errorf("want both lines damaged, and the head's count to agree, got %q", got)
	}
	f.reopen()
	if why := f.pausedWhy("gage"); !strings.Contains(why, "fails its seal") {
		t.Errorf("the edit must pause the talkoot, got %q", why)
	}
	f.resume("")
	f.reopen()
	if why := f.pausedWhy("gage"); why != "" {
		t.Errorf("the resume chains from the last good line and must hold, got %q", why)
	}
}

// Lines removed from the middle break the chain at the line after the gap.
// That is one seal failure however many lines went, so the head's count says
// how many.
func TestARemovedLineBreaksTheChain(t *testing.T) {
	f := newFixture(t, nil)
	for _, m := range []string{"jev", "gage", "atlas", "helm"} {
		if err := f.router.TurnEnded(m, 1); err != nil {
			t.Fatal(err)
		}
	}
	lines := f.rawLines()
	f.writeRaw(append(lines[:1:1], lines[3:]...))
	got := f.damagedReasons()
	if len(got) != 2 || !strings.Contains(got[0], "line 2 of the room fails its seal") ||
		!strings.Contains(got[1], "3 of the 4 lines") {
		t.Errorf("want the line after the gap damaged, and the count of what went, got %q", got)
	}
}

// 🔑 What remains after a cut is still a valid chain. The head names the last
// line the router wrote, so the cut shows.
func TestLinesCutFromTheEndAreCaught(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnEnded("jev", 20); err != nil {
		t.Fatal(err)
	}
	lines := f.rawLines()
	f.writeRaw(lines[:1])
	if got := f.damagedReasons(); len(got) != 1 || !strings.Contains(got[0], "cut from its end") {
		t.Errorf("want the cut caught, got %q", got)
	}
	if err := os.Remove(OpenRoom(f.dir).Path()); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	if why := f.pausedWhy("jev"); !strings.Contains(why, "cut from its end") {
		t.Errorf("a deleted room must pause the talkoot, got %q", why)
	}
}

func TestAMissingHeadIsCaught(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	if err := os.Remove(filepath.Join(f.dir, HeadFile)); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	if why := f.pausedWhy("helm"); !strings.Contains(why, "head record is missing") {
		t.Errorf("want the talkoot paused, got %q", why)
	}
}

// A room whose key is gone cannot verify its past. A new key seals what comes
// next, and the talkoot stays paused until a person resumes it.
func TestALostKeyPausesAndStartsANewChain(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	if err := os.Remove(filepath.Join(f.dir, KeyFile)); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	if why := f.pausedWhy("helm"); !strings.Contains(why, "key was missing") {
		t.Fatalf("want the talkoot paused, got %q", why)
	}
	f.resume("")
	f.reopen()
	if why := f.pausedWhy("helm"); why != "" {
		t.Errorf("the resume is sealed under the new key and must hold, got %q", why)
	}
}

func TestAKeyThatDoesNotParseStopsTheRoom(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	if err := os.WriteFile(filepath.Join(f.dir, KeyFile), []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewRouter(f.roster, OpenRoom(f.dir), Drivers{Native: f.native, Worker: f.worker}, f.limits, f.clock.now)
	if err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("want the router refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, KeyFile)); errors.Is(err, os.ErrNotExist) {
		t.Error("the bad key must be left for a person to look at")
	}
}

func (f *fixture) status(member string) Status {
	for _, s := range f.router.Statuses() {
		if s.Member == member {
			return s
		}
	}
	f.t.Fatalf("no status for %s", member)
	return Status{}
}

// 🚨 A room opened without a key cannot tell a new talkoot from one deleted to
// clear its spend. Either way it starts paused, and the pause is a sealed line
// that holds across restarts.
func TestARoomWithNoKeyStartsPaused(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnEnded("jev", 20); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{RoomFile, KeyFile, HeadFile} {
		if err := os.Remove(filepath.Join(f.dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, when := range []string{"live", "after a restart"} {
		f.reopen()
		if why := f.pausedWhy("jev"); !strings.Contains(why, "the room had no key") {
			t.Errorf("%s: want the talkoot paused, got %q", when, why)
		}
	}
	f.resume("")
	f.reopen()
	if why := f.pausedWhy("jev"); why != "" {
		t.Errorf("a person's resume starts it, got %q", why)
	}
}

func TestCreateRoomStartsUnpausedAndOnlyOnce(t *testing.T) {
	f := newFixture(t, nil)
	if why := f.pausedWhy("helm"); why != "" {
		t.Errorf("a created room starts unpaused, got %q", why)
	}
	if err := CreateRoom(f.dir); err == nil {
		t.Error("CreateRoom must refuse a room that has a key")
	}
}

// plantDir puts a directory where a file goes, so a write there fails. It
// returns what it moved aside, for restore.
func (f *fixture) plantDir(name string) (restore func()) {
	f.t.Helper()
	path := filepath.Join(f.dir, name)
	aside := path + ".aside"
	if err := os.Rename(path, aside); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return func() {
		f.t.Helper()
		if err := os.RemoveAll(path); err != nil {
			f.t.Fatal(err)
		}
		if err := os.Rename(aside, path); err != nil {
			f.t.Fatal(err)
		}
	}
}

// 🚨 A line written without its head could be cut, with every line after
// it, and the room would still match the older head. So a head that cannot
// be written keeps the line out, and the router treats it as a failed append.
func TestAHeadThatCannotBeWrittenKeepsTheLineOut(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	before := f.rawLines()
	restore := f.plantDir(HeadFile)
	if err := f.router.TurnEnded("helm", 0.1); err == nil {
		t.Fatal("want the turn to fail when its head cannot be written")
	}
	if why := f.pausedWhy("helm"); !strings.Contains(why, "could not record a turn") {
		t.Fatalf("want the talkoot paused, got %q", why)
	}
	restore()
	if after := f.rawLines(); len(after) != len(before) {
		t.Fatalf("a line landed without its head: %d lines, want %d", len(after), len(before))
	}
	if got := f.damagedReasons(); len(got) != 0 {
		t.Errorf("the room and its head still agree, got %q", got)
	}
}

// A line that fails after its head was written leaves the head ahead of the
// room. That reads as a cut, and a cut pauses the talkoot.
func TestALineThatFailsAfterItsHeadReadsAsACut(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	restore := f.plantDir(RoomFile)
	if err := f.router.TurnEnded("helm", 0.1); err == nil {
		t.Fatal("want the turn to fail when the room cannot be written")
	}
	restore()
	got := f.damagedReasons()
	if len(got) != 1 || !strings.Contains(got[0], "cut from its end") {
		t.Fatalf("want the missing line read as a cut, got %q", got)
	}
	f.reopen()
	if why := f.pausedWhy("helm"); !strings.Contains(why, "cut from its end") {
		t.Errorf("want the talkoot paused after a restart, got %q", why)
	}
	f.resume("")
	f.reopen()
	if why := f.pausedWhy("helm"); why != "" {
		t.Errorf("the resume's line and head agree again, so the pause lifts, got %q", why)
	}
}

// 🚨 The head's temporary file has a random name. A symlink a member plants at
// a predictable name is never followed.
func TestTheHeadIsNotWrittenThroughAPlantedSymlink(t *testing.T) {
	f := newFixture(t, nil)
	target := filepath.Join(testsupport.TempDir(t), "victim")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(f.dir, HeadTempFile)); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	f.post()
	if b, _ := os.ReadFile(target); string(b) != "untouched" {
		t.Errorf("the head write followed the planted link: %q", b)
	}
	if why := f.pausedWhy("helm"); why != "" {
		t.Errorf("the planted link must not block the head, got %q", why)
	}
}

// 🚨 A member copies a genuine pair of lines, the line before a person's
// resume and the resume, and appends both after a damaged line. The copied
// resume must not lift the pause the damage caused.
func TestAReplayedResumeCannotLiftADamagePause(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnEnded("jev", 1); err != nil {
		t.Fatal(err)
	}
	f.resume("")
	lines := f.rawLines()
	pair := [][]byte{lines[len(lines)-2], lines[len(lines)-1]}
	f.writeRaw(append(append(lines, []byte(`{"type":"turn","member":"jev","cost_usd":9}`)), pair...))
	f.reopen()
	if why := f.pausedWhy("jev"); !strings.Contains(why, "carries no seal") {
		t.Errorf("the replayed resume must not lift the damage pause, got %q", why)
	}
}

// 🚨 A read while the router runs, from a view that shows the room, must not
// move where the next line goes. If it did, a member could cut the end, wait
// for a read, and let the next append seal over the cut for good.
func TestAReadAfterACutDoesNotMoveTheAppendPosition(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	r := OpenRoom(dir)
	for _, m := range []string{"jev", "gage", "lens"} {
		if err := r.Append(Line{Type: LineTurn, Member: m, CostUSD: 1}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if err := os.WriteFile(r.Path(), bytes.Join(lines[:2], nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(Line{Type: LineTurn, Member: "helm", CostUSD: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := OpenRoom(dir).Read()
	if err != nil {
		t.Fatal(err)
	}
	damaged := 0
	for _, l := range got {
		if l.Type == LineDamaged {
			damaged++
		}
	}
	if damaged == 0 {
		t.Errorf("the cut vanished after a read and an append: %+v", got)
	}
}

// 🚨 A damaged entry is never written, and the next append rewrites the head.
// Without a sealed pause, one post after the restart would make a cut clean.
func TestADamagePauseOutlivesTheNextAppend(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnEnded("jev", 20); err != nil {
		t.Fatal(err)
	}
	f.writeRaw(f.rawLines()[:1])
	f.reopen()
	why := f.pausedWhy("jev")
	if !strings.Contains(why, "cut from its end") || !strings.Contains(why, "a resume goes on without it") {
		t.Fatalf("want the cut paused, with what a resume costs, got %q", why)
	}
	f.post()
	f.reopen()
	if why := f.pausedWhy("jev"); !strings.Contains(why, "cut from its end") {
		t.Errorf("the pause must survive an append and a restart, got %q", why)
	}
}

// A failed line removed after the restart, once the router has written past
// it, still leaves the sealed pause behind.
func TestAFailedLineRemovedLaterStaysPaused(t *testing.T) {
	f := newFixture(t, nil)
	for _, m := range []string{"jev", "gage", "helm"} {
		if err := f.router.TurnEnded(m, 5); err != nil {
			t.Fatal(err)
		}
	}
	lines := f.rawLines()
	lines[1] = bytes.Replace(lines[1], []byte(`"cost_usd":5`), []byte(`"cost_usd":0.5`), 1)
	f.writeRaw(lines)
	f.reopen()
	f.post()
	lines = f.rawLines()
	f.writeRaw(append([][]byte{lines[0]}, lines[3:]...))
	if got := f.damagedReasons(); len(got) != 0 {
		t.Fatalf("the removal leaves a clean chain, got %q", got)
	}
	f.reopen()
	if why := f.pausedWhy("jev"); !strings.Contains(why, "line 2 of the room fails its seal") {
		t.Errorf("want the sealed pause to hold, got %q", why)
	}
}

// A room whose head cannot be made loses its key too, so a retry starts over.
func TestCreateRoomCanBeRetriedAfterItsHeadFails(t *testing.T) {
	dir := testsupport.TempDir(t)
	blocker := filepath.Join(dir, HeadTempFile)
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CreateRoom(dir); err == nil {
		t.Fatal("want CreateRoom to fail while its head cannot be written")
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want the key removed, got %v", err)
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	if err := CreateRoom(dir); err != nil {
		t.Errorf("a retry must succeed: %v", err)
	}
}
