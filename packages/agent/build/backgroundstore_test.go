package build

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func minPNG() []byte  { return append([]byte("\x89PNG\r\n\x1a\n"), 0, 1, 2, 3) }
func minJPEG() []byte { return append([]byte{0xFF, 0xD8, 0xFF}, 0, 1, 2, 3) }
func minGIF() []byte  { return append([]byte("GIF89a"), 0, 1, 2, 3) }
func minWEBP() []byte {
	b := append([]byte("RIFF"), 0, 0, 0, 0)
	b = append(b, []byte("WEBP")...)
	return append(b, 0, 1, 2, 3)
}

func TestBackgroundStoreImportFormats(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewBackgroundStore()

	for ext, data := range map[string][]byte{"png": minPNG(), "jpg": minJPEG(), "gif": minGIF(), "webp": minWEBP()} {
		b, err := st.ImportBytes(data)
		if err != nil {
			t.Fatalf("%s import: %v", ext, err)
		}
		if b.Ext != ext {
			t.Errorf("%s stored as ext %q", ext, b.Ext)
		}
		if st.Path(b.ID) == "" {
			t.Errorf("%s: Path empty after import", ext)
		}
	}
	if bgs, err := st.List(); err != nil || len(bgs) != 4 {
		t.Fatalf("list: %d, %v (want 4)", len(bgs), err)
	}
}

func TestBackgroundStoreRejectsUnknownFormat(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if _, err := NewBackgroundStore().ImportBytes([]byte("this is not an image")); err == nil {
		t.Error("importing a non-image should error")
	}
}

func TestBackgroundStoreIdempotentAndDelete(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewBackgroundStore()

	a, err := st.ImportBytes(minPNG())
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.ImportBytes(minPNG())
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Errorf("re-import must be idempotent: %q != %q", a.ID, b.ID)
	}
	if bgs, _ := st.List(); len(bgs) != 1 {
		t.Errorf("idempotent import should not duplicate: %d", len(bgs))
	}

	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if st.Path(a.ID) != "" {
		t.Error("Path should be empty after delete")
	}
	if err := st.Delete(a.ID); err == nil {
		t.Error("double delete should error")
	}
}

func TestBackgroundStoreRejectsBadID(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewBackgroundStore()
	for _, bad := range []string{"", "../etc", "abc/def", "XYZ", "nothex", "deadbeef.."} {
		if p := st.Path(bad); p != "" {
			t.Errorf("Path(%q) should be empty (invalid id), got %q", bad, p)
		}
	}
}
