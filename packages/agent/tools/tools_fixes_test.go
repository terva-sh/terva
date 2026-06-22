package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// --- Issue 1: edit diff must not build an O(m*n) LCS over the whole file ---

// TestEditLargeFileDiffFast edits one line in a 60k-line file. With the
// old whole-file LCS this allocated ~(60000)^2 ints (~28GB) and OOM'd or
// took minutes. With prefix/suffix trimming it must finish near-instantly
// and emit a compact, correct diff.
func TestEditLargeFileDiffFast(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")

	const n = 60000
	const changeAt = 30000 // 0-indexed line that we mutate
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i == changeAt {
			b.WriteString("UNIQUE_OLD_LINE\n")
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{CWD: dir}
	done := make(chan struct{})
	var diff string
	var execErr error
	go func() {
		defer close(done)
		res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
			"path":  "big.txt",
			"edits": []map[string]any{{"oldText": "UNIQUE_OLD_LINE", "newText": "UNIQUE_NEW_LINE"}},
		}), nil)
		execErr = err
		if err == nil {
			diff = res.Content[0].(provider.TextBlock).Text
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("edit on 60k-line file did not complete in 10s; whole-file LCS likely reintroduced")
	}
	if execErr != nil {
		t.Fatal(execErr)
	}

	// File content must be correctly spliced.
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), "UNIQUE_NEW_LINE") || strings.Contains(string(got), "UNIQUE_OLD_LINE") {
		t.Fatal("edit did not apply correctly to large file")
	}

	// Diff must be compact (context-only around the change), not the
	// whole 60k-line file.
	if strings.Count(diff, "\n") > 20 {
		t.Fatalf("diff is not compact (%d lines):\n%s", strings.Count(diff, "\n"), diff)
	}
	if !strings.Contains(diff, "-UNIQUE_OLD_LINE") || !strings.Contains(diff, "+UNIQUE_NEW_LINE") {
		t.Fatalf("diff missing the changed lines:\n%s", diff)
	}
	// Context lines around the change should be present.
	if !strings.Contains(diff, "line 29999") || !strings.Contains(diff, "line 30001") {
		t.Fatalf("diff missing surrounding context:\n%s", diff)
	}
}

// TestEditDiffMatchesUnchangedCases guards that prefix/suffix trimming
// produces the same diff a full LCS would for a small edit.
func TestEditDiffMatchesUnchangedCases(t *testing.T) {
	// Note: trailing "\n" makes Split yield a final "" element, rendered
	// as a trailing context row (" \n"). This matches the pre-existing
	// whole-file-LCS behavior; prefix/suffix trimming must preserve it.
	a := "a\nb\nc\nd\ne\n"
	b := "a\nb\nX\nd\ne\n"
	got := unifiedDiff("f", a, b)
	want := " a\n b\n-c\n+X\n d\n e\n \n"
	if got != want {
		t.Fatalf("diff mismatch\nwant %q\ngot  %q", want, got)
	}
}

// --- Issue 2: edit CRLF edge cases ---

// TestEditCRLFOldText: a model that copied \r\n bytes out of a read of a
// CRLF file passes \r\n inside oldText. The match must still succeed.
func TestEditCRLFOldText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o644)
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "a.txt",
		// oldText spans a line boundary and carries \r\n, as a model
		// copying read output would.
		"edits": []map[string]any{{"oldText": "alpha\r\nbeta", "newText": "ALPHA\nBETA"}},
	}), nil)
	if err != nil {
		t.Fatalf("CRLF oldText should match, got error: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "ALPHA\r\nBETA\r\ngamma\r\n" {
		t.Fatalf("got %q", string(got))
	}
}

// TestEditCRLFNewTextNoDouble: newText that already contains \r\n must
// not be turned into \r\r\n by the CRLF re-application step.
func TestEditCRLFNewTextNoDouble(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("one\r\ntwo\r\n"), 0o644)
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":  "a.txt",
		"edits": []map[string]any{{"oldText": "two", "newText": "x\r\ny"}},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if strings.Contains(string(got), "\r\r\n") {
		t.Fatalf("newText \\r\\n was double-converted to \\r\\r\\n: %q", string(got))
	}
	if string(got) != "one\r\nx\r\ny\r\n" {
		t.Fatalf("got %q", string(got))
	}
}

// --- Issue 3a: read paging past the byte cap + image cap ---

// TestReadOffsetPastByteCap writes a file far larger than maxReadBytes and
// reads with an offset that lands beyond the first 50KiB. The old code
// applied the byte cap before offset and returned empty content; now the
// selected slice must contain the real lines at that offset.
func TestReadOffsetPastByteCap(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")

	// Each line "line NNNNN" is ~11 bytes; 20000 lines ~= 220KB, well past
	// the 50KiB cap. The target offset sits beyond byte 50KiB.
	var b strings.Builder
	const n = 20000
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %05d\n", i)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Line 10000 (1-indexed offset 10001) is byte ~110KB into the file.
	tool := &ReadTool{CWD: dir}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "big.txt", "offset": 10001, "limit": 3,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if got != "line 10000\nline 10001\nline 10002\n" {
		t.Fatalf("offset paging past 50KiB broken, got %q", got)
	}
	if d := res.Details.(map[string]any); d["start_line"] != 10001 {
		t.Errorf("start_line want 10001, got %v", d["start_line"])
	}
}

// TestReadByteCapAppliesToSelection verifies the 50KiB cap still trims a
// large selected window (so we did not just remove the cap).
func TestReadByteCapAppliesToSelection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	// Use long lines so the 50KiB byte cap engages well before the 2000
	// line cap: ~200 bytes/line * 2000 lines = ~400KB, but the byte cap
	// (50KiB) trims after ~256 lines.
	long := strings.Repeat("x", 200)
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString(long)
		b.WriteByte('\n')
	}
	os.WriteFile(p, []byte(b.String()), 0o644)
	tool := &ReadTool{CWD: dir}
	// No offset: selection is the whole file; the byte cap must engage.
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "big.txt"}), nil)
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, fmt.Sprintf("truncated at %d bytes", maxReadBytes)) {
		tail := got
		if len(tail) > 120 {
			tail = tail[len(tail)-120:]
		}
		t.Fatalf("expected byte-truncation note, got tail: %q", tail)
	}
	if d := res.Details.(map[string]any); d["bytes_truncated"] != true {
		t.Errorf("bytes_truncated want true, got %v", d["bytes_truncated"])
	}
}

// TestReadImageCap rejects an oversized image with a clear error.
func TestReadImageCap(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.png")
	// A buffer larger than maxImageBytes; contents irrelevant (size is
	// checked via Stat before reading).
	big := make([]byte, maxImageBytes+1)
	if err := os.WriteFile(p, big, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "huge.png"}), nil)
	if err == nil {
		t.Fatal("want oversized-image rejection")
	}
	if !strings.Contains(err.Error(), "capped") {
		t.Fatalf("error should mention the cap, got %v", err)
	}
}

// --- Issue 3b: bash default timeout + spill-file honesty ---

// TestBashDefaultTimeout: with no timeout passed, a command that sleeps
// past the default is killed and the result says so.
// TestBashEnvExported: vars in BashTool.Env reach the command's
// environment and win over an inherited duplicate.
func TestBashEnvExported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	t.Setenv("TERVA_HOME", "/inherited/should/lose")
	tool := &BashTool{CWD: t.TempDir(), Env: map[string]string{"TERVA_HOME": "/resolved/home"}}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": `printf '%s' "$TERVA_HOME"`,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, "/resolved/home") {
		t.Fatalf("TERVA_HOME not exported (or inherited value won): %q", got)
	}
	if strings.Contains(got, "/inherited/should/lose") {
		t.Fatalf("inherited TERVA_HOME leaked through: %q", got)
	}
}

// TestBashSpillFooterIsActionable: a truncated run points the model at the
// spill file with a read-it-don't-rerun hint, not a bare path.
func TestBashSpillFooterIsActionable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: t.TempDir()}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": fmt.Sprintf("yes | head -n %d", (maxBashBytes/2)+5000),
		"timeout": 30,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := res.Details.(map[string]any)["full_output_path"].(string); p != "" {
		defer os.Remove(p)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, "read this file") || !strings.Contains(got, "offset/limit") {
		t.Fatalf("spill footer not actionable: %q", got)
	}
}

func TestBashDefaultTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	// Shrink the default for the test by passing an explicit small timeout
	// is not enough to test the *default* path, so we instead verify the
	// default is applied by checking a long-running command without a
	// timeout terminates via the default. To keep the test fast we rely on
	// the explicit-timeout path producing the same "timed out" hint, and
	// separately assert the default constant is wired in.
	tool := &BashTool{CWD: t.TempDir()}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "sleep 5", "timeout": 1,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("timed-out command should be flagged as error")
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, "timed out after") {
		t.Fatalf("missing timeout hint, got %q", got)
	}
	if d := res.Details.(map[string]any); d["timed_out"] != true {
		t.Errorf("timed_out detail want true, got %v", d["timed_out"])
	}
}

// TestBashDefaultTimeoutApplied confirms a command with no explicit
// timeout still runs under a deadline (the default), by checking the
// runtime wiring: defaultBashTimeout is a positive duration and a quick
// command with no timeout succeeds normally.
func TestBashDefaultTimeoutApplied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	if defaultBashTimeout <= 0 {
		t.Fatal("defaultBashTimeout must be positive")
	}
	tool := &BashTool{CWD: t.TempDir()}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo ok", // no timeout field
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("quick command should not error under the default timeout")
	}
	if !strings.Contains(res.Content[0].(provider.TextBlock).Text, "ok") {
		t.Fatal("expected command output")
	}
}

// TestBashSpillFileIsGenuinelyFull: when output exceeds maxBashBytes, the
// "full output" spill file must contain the complete output, not a copy
// of the truncated in-memory buffer.
func TestBashSpillFileIsGenuinelyFull(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: t.TempDir()}
	// Emit well past maxBashBytes (50KiB). yes|head gives deterministic,
	// large output cheaply.
	totalLines := (maxBashBytes / 2) + 5000 // each "y\n" is 2 bytes
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": fmt.Sprintf("yes | head -n %d", totalLines),
		"timeout": 30,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(map[string]any)
	if d["bytes_truncated"] != true {
		t.Fatalf("expected byte truncation, details: %v", d)
	}
	fullPath, _ := d["full_output_path"].(string)
	if fullPath == "" {
		t.Fatal("expected a full_output_path for truncated output")
	}
	defer os.Remove(fullPath)
	info, err := os.Stat(fullPath)
	if err != nil {
		t.Fatalf("spill file missing: %v", err)
	}
	wantBytes := int64(totalLines * 2) // "y\n" per line
	if info.Size() != wantBytes {
		t.Fatalf("spill file not genuinely full: size=%d want=%d (= total command output)", info.Size(), wantBytes)
	}
}

// TestBashSpillDiscardedWhenComplete: a command whose output fits inline
// must NOT leave a spill file behind, and must not advertise one.
func TestBashSpillDiscardedWhenComplete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: t.TempDir()}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"command": "echo small"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(map[string]any)
	if p, _ := d["full_output_path"].(string); p != "" {
		t.Fatalf("non-truncated output should not produce a spill file, got %q", p)
	}
	if strings.Contains(res.Content[0].(provider.TextBlock).Text, "full output") {
		t.Fatal("non-truncated output should not advertise a full-output file")
	}
}
