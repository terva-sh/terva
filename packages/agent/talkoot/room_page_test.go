package talkoot

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// pageRoom makes a room with n sealed lines, and returns its directory.
func pageRoom(t *testing.T, n int) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	if err := CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	r := OpenRoom(dir)
	for i := range n {
		if err := r.Append(Line{Type: LineTurn, Member: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// rewriteRoomLine replaces line n (from 1) of the room file with fn of it.
func rewriteRoomLine(t *testing.T, dir string, n int, fn func([]byte) []byte) {
	t.Helper()
	path := OpenRoom(dir).Path()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(b, []byte("\n"))
	lines[n-1] = fn(lines[n-1])
	if err := os.WriteFile(path, bytes.Join(lines, []byte("\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// flipMAC changes one character near the end of a sealed line, keeping its
// length, so the line no longer verifies.
func flipMAC(raw []byte) []byte {
	out := bytes.Clone(raw)
	i := bytes.LastIndexByte(out, '"') - 1
	if out[i] == 'A' {
		out[i] = 'B'
	} else {
		out[i] = 'A'
	}
	return out
}

// checkPages asserts that every page r serves is the slice of want that Read
// gives, with the same first index and total.
func checkPages(t *testing.T, name string, r *Room, want []Line) {
	t.Helper()
	total := len(want)
	for _, before := range []int{0, 1, 5, 63, 64, 65, 128, total - 1, total, total + 5} {
		for _, limit := range []int{1, 10, 64, 100, 1000} {
			got, start, n, err := r.Page(before, limit)
			if err != nil {
				t.Fatalf("%s: Page(%d, %d): %v", name, before, limit, err)
			}
			ws, we := pageBounds(total, before, limit)
			if n != total || start != ws || len(got) != we-ws {
				t.Fatalf("%s: Page(%d, %d) = %d lines from %d of %d, want %d from %d of %d",
					name, before, limit, len(got), start, n, we-ws, ws, total)
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], want[ws+i]) {
					t.Fatalf("%s: Page(%d, %d) entry %d = %+v, want %+v", name, before, limit, ws+i, got[i], want[ws+i])
				}
			}
		}
	}
}

// A page is exactly the slice of Read it stands for, across checkpoints, over
// damaged lines, over a cut end, and after appends that move the index on.
func TestAPageIsTheSliceOfReadItStandsFor(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 65, 200} {
		dir := pageRoom(t, n)
		checkPages(t, fmt.Sprintf("%d lines", n), OpenRoom(dir), readAll(t, dir))
	}

	// A forged line in the middle, and every line after it, reads as damaged.
	dir := pageRoom(t, 150)
	rewriteRoomLine(t, dir, 70, flipMAC)
	want := readAll(t, dir)
	if want[69].Type != LineDamaged || want[149].Type != LineDamaged {
		t.Fatalf("the probe is void: the forged room reads %+v, then %+v", want[69], want[149])
	}
	checkPages(t, "a forged line", OpenRoom(dir), want)

	// A cut end adds an entry after the lines, and the next append clears it.
	dir = pageRoom(t, 100)
	path := OpenRoom(dir).Path()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if err := os.WriteFile(path, bytes.Join(lines[:90], nil), 0o600); err != nil {
		t.Fatal(err)
	}
	want = readAll(t, dir)
	if len(want) != 91 || !strings.Contains(want[90].Reason, "cut") {
		t.Fatalf("the probe is void: the cut room reads %d entries, the last %+v", len(want), want[len(want)-1])
	}
	r := OpenRoom(dir)
	checkPages(t, "a cut end", r, want)
	for i := range 70 {
		if err := r.Append(Line{Type: LineTurn, Member: fmt.Sprintf("n%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	checkPages(t, "appends after a cut", r, readAll(t, dir))
}

// A torn last line gets its newline from the next append, and the index still
// finds the lines after it. The torn line is entry 63, so the first append
// after it lands on a checkpoint.
func TestAPageAfterATornLine(t *testing.T) {
	dir := pageRoom(t, 63)
	path := OpenRoom(dir).Path()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, []byte(`{"torn`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	r := OpenRoom(dir)
	for i := range 3 {
		if err := r.Append(Line{Type: LineTurn, Member: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	want := readAll(t, dir)
	if want[63].Type != LineDamaged || want[64].Member != "t0" {
		t.Fatalf("the probe is void: %+v, %+v", want[63], want[64])
	}
	checkPages(t, "a torn line", r, want)
}

// The index that appends build is the index a fresh scan builds, after a torn
// line too. A wrong offset would not show in a page, because the page would
// go back to a full scan, so this compares the checkpoints themselves.
func TestAppendsIndexTheirLinesAsAScanWould(t *testing.T) {
	dir := pageRoom(t, 63)
	path := OpenRoom(dir).Path()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, []byte(`{"torn`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	r := OpenRoom(dir)
	for i := range 140 {
		if err := r.Append(Line{Type: LineTurn, Member: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	fresh := OpenRoom(dir)
	if _, _, _, err := fresh.Page(0, 1); err != nil {
		t.Fatal(err)
	}
	if r.stale || len(r.checkpoints) != 4 || !reflect.DeepEqual(r.checkpoints, fresh.checkpoints) {
		t.Fatalf("the appends' index (stale %v) is %+v, want the scan's %+v", r.stale, r.checkpoints, fresh.checkpoints)
	}
}

// A page reads the file from its own checkpoint on, not from the start. A
// line damaged on disk after load, before that checkpoint, is outside every
// page after it. A full read, the cost the page avoids, still sees it.
func TestAPageReadsFromItsCheckpoint(t *testing.T) {
	dir := pageRoom(t, 300)
	r := OpenRoom(dir)
	if _, _, _, err := r.Page(0, 10); err != nil {
		t.Fatal(err)
	}
	rewriteRoomLine(t, dir, 2, flipMAC)
	if all := readAll(t, dir); all[299].Type != LineDamaged {
		t.Fatal("the probe is void: a full read does not see the damaged line")
	}
	newest, _, _, err := r.Page(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range newest {
		if l.Type == LineDamaged {
			t.Fatalf("the newest page read the start of the room: %+v", l)
		}
	}
	// A page in the first checkpoint's range reads the damaged line.
	early, _, _, err := r.Page(20, 10)
	if err != nil {
		t.Fatal(err)
	}
	if early[0].Type != LineDamaged {
		t.Fatalf("a page from checkpoint 0 missed the damaged line: %+v", early[0])
	}
}

// A room that lost lines after load says so on the page that needs them,
// rather than returning a short page.
func TestAPageOverLinesCutAfterLoadSaysSo(t *testing.T) {
	dir := pageRoom(t, 100)
	r := OpenRoom(dir)
	if _, _, _, err := r.Page(0, 10); err != nil {
		t.Fatal(err)
	}
	path := r.Path()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if err := os.WriteFile(path, bytes.Join(lines[:95], nil), 0o600); err != nil {
		t.Fatal(err)
	}
	got, start, total, err := r.Page(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if start != 90 || total != 100 || len(got) != 6 || !strings.Contains(got[5].Reason, "cut") {
		t.Fatalf("page over a cut: %d lines from %d of %d, last %+v", len(got), start, total, got[len(got)-1])
	}
}

func readAll(t *testing.T, dir string) []Line {
	t.Helper()
	lines, err := OpenRoom(dir).Read()
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// A line before a checkpoint that changes length after load moves every line
// after it. The page notices that its checkpoint no longer starts a line, and
// serves the page from a full scan, which rebuilds the index.
func TestAPageAfterALineChangesLength(t *testing.T) {
	dir := pageRoom(t, 200)
	r := OpenRoom(dir)
	if _, _, _, err := r.Page(0, 10); err != nil {
		t.Fatal(err)
	}
	rewriteRoomLine(t, dir, 2, func(raw []byte) []byte { return append([]byte(`{"x":1}`), raw...) })
	want := readAll(t, dir)
	got, start, total, err := r.Page(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(want) || start != len(want)-10 || !reflect.DeepEqual(got, want[start:]) {
		t.Fatalf("the page after a length change is %d lines from %d of %d, want the tail of a full read of %d", len(got), start, total, len(want))
	}
	if r.stale || !r.indexed || r.entries != 200 {
		t.Fatalf("the index was not rebuilt: stale %v, indexed %v, entries %d", r.stale, r.indexed, r.entries)
	}
	checkPages(t, "after a length change", r, want)
}

// A line added before a checkpoint can leave the checkpoint's offset at the
// start of another line, where the newline check passes. The page sees that
// the line there is not the checkpoint's, and goes back to a full scan.
func TestAPageAfterALineIsAddedBeforeItsCheckpoint(t *testing.T) {
	dir := pageRoom(t, 200)
	r := OpenRoom(dir)
	if _, _, _, err := r.Page(0, 10); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Checkpoint 1 is file line 65. A line as long as line 64 moves line 64 to
	// where line 65 started.
	prev := bytes.Split(b, []byte("\n"))[63]
	rewriteRoomLine(t, dir, 2, func(raw []byte) []byte {
		return append(append(bytes.Repeat([]byte("x"), len(prev)), '\n'), raw...)
	})
	r.mu.Lock()
	cp := r.checkpoints[1]
	r.mu.Unlock()
	if b, err = os.ReadFile(r.Path()); err != nil {
		t.Fatal(err)
	}
	if b[cp.off-1] != '\n' || !bytes.HasPrefix(b[cp.off:], prev) {
		t.Fatalf("the test did not move line 64 onto checkpoint 1's offset")
	}
	want := readAll(t, dir)
	// Entries 90 to 99 read from checkpoint 1.
	got, start, total, err := r.Page(100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(want) || !reflect.DeepEqual(got, want[start:start+len(got)]) {
		t.Fatalf("the page after an added line is %d lines from %d of %d, want the slice of a full read of %d", len(got), start, total, len(want))
	}
	if r.stale || !r.indexed || r.entries != len(want) {
		t.Fatalf("the index was not rebuilt: stale %v, indexed %v, entries %d", r.stale, r.indexed, r.entries)
	}
	checkPages(t, "after an added line", r, want)
}

// A stale index, after an append that failed part way, goes back to a full
// scan on the next page, which rebuilds the index and clears the flag.
func TestAStaleIndexIsRebuiltByTheNextPage(t *testing.T) {
	dir := pageRoom(t, 100)
	r := OpenRoom(dir)
	if _, _, _, err := r.Page(0, 10); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.stale, r.checkpoints = true, nil
	r.mu.Unlock()
	checkPages(t, "a stale index", r, readAll(t, dir))
	if r.stale || len(r.checkpoints) != 2 {
		t.Fatalf("the index was not rebuilt: stale %v, %d checkpoints", r.stale, len(r.checkpoints))
	}
}
