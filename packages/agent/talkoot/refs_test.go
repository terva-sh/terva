package talkoot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"terva.sh/terva/packages/testsupport"
)

func refFixture(t *testing.T) (dir, home string) {
	t.Helper()
	dir, home = testsupport.TempDir(t), testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "docs", "plan.md"), []byte("# The plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, home
}

func TestReadRefOpensAPathAndANote(t *testing.T) {
	dir, home := refFixture(t)
	got, err := ReadRef(dir, home, "path:docs/plan.md")
	if err != nil || got.Text != "# The plan\n" || got.Truncated || got.Binary {
		t.Fatalf("path: = %+v, %v", got, err)
	}
	if _, err := WriteNote(dir, "helm", "report.md", "all green"); err != nil {
		t.Fatal(err)
	}
	got, err = ReadRef(dir, home, "note:helm/report.md")
	if err != nil || got.Text != "all green" {
		t.Fatalf("note: = %+v, %v", got, err)
	}
}

// 🚨 The router checked the shape when the member sent it. The file can be
// swapped for a link afterwards, so the read itself must refuse to leave the
// checkout.
func TestReadRefFollowsNoLinkOutOfTheCheckout(t *testing.T) {
	dir, home := refFixture(t)
	outside := filepath.Join(testsupport.TempDir(t), "secret.txt")
	if err := os.WriteFile(outside, []byte("not for the team"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "docs", "leak.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, err := ReadRef(dir, home, "path:docs/leak.md"); err == nil || strings.Contains(got.Text, "not for the team") {
		t.Fatalf("a link out of the checkout was read: %+v, %v", got, err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(home, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRef(dir, home, "path:out/secret.txt"); err == nil {
		t.Fatal("a directory link out of the checkout was followed")
	}
	// A link that stays inside the checkout is the checkout's own file.
	if err := os.Symlink("plan.md", filepath.Join(home, "docs", "alias.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRef(dir, home, "path:docs/alias.md"); err != nil || got.Text != "# The plan\n" {
		t.Fatalf("a link inside the checkout = %+v, %v", got, err)
	}
}

func TestReadRefRefusesWhatIsNotAFileInTheCheckout(t *testing.T) {
	dir, home := refFixture(t)
	for _, ref := range []string{"path:../x", "path:/etc/passwd", "path:docs", "path:docs/missing.md", "ticket:TKT-1", "url:https://x"} {
		if _, err := ReadRef(dir, home, ref); err == nil {
			t.Errorf("%s was read", ref)
		}
	}
}

func TestReadRefCapsALargeFileAndMarksABinaryOne(t *testing.T) {
	dir, home := refFixture(t)
	big := strings.Repeat("€", MaxRefBytes) // three bytes each, and the cap is not a multiple of three
	if err := os.WriteFile(filepath.Join(home, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRef(dir, home, "path:big.txt")
	if err != nil || !got.Truncated || got.Binary || len(got.Text) > MaxRefBytes || got.Size != int64(len(big)) || !utf8.ValidString(got.Text) {
		t.Fatalf("big file = truncated %v binary %v len %d size %d, %v", got.Truncated, got.Binary, len(got.Text), got.Size, err)
	}
	if err := os.WriteFile(filepath.Join(home, "blob.bin"), []byte{0x7f, 'E', 'L', 'F', 0, 1}, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = ReadRef(dir, home, "path:blob.bin")
	if err != nil || !got.Binary || got.Text != "" {
		t.Fatalf("binary file = %+v, %v", got, err)
	}
}
