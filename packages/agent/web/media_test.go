//go:build terva_web

package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/testsupport"
)

// mediaCharaPNG builds a minimal PNG carrying a card in a base64 `chara` chunk,
// so a store import retains it as the avatar the /media route then serves.
func mediaCharaPNG(cardJSON string) []byte {
	chunk := func(ctype string, data []byte) []byte {
		var b bytes.Buffer
		var lenb [4]byte
		binary.BigEndian.PutUint32(lenb[:], uint32(len(data)))
		b.Write(lenb[:])
		b.WriteString(ctype)
		b.Write(data)
		b.Write([]byte{0, 0, 0, 0}) // CRC placeholder (ReadPNG ignores it)
		return b.Bytes()
	}
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	b.Write(chunk("tEXt", []byte("chara\x00"+base64.StdEncoding.EncodeToString([]byte(cardJSON)))))
	b.Write(chunk("IEND", nil))
	return b.Bytes()
}

func authedGet(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestMediaServesCardAvatarBehindAuth is the phase-2 proof that an imported PNG's
// portrait is reachable over HTTP — gated, byte-faithful, and 404 for the misses.
func TestMediaServesCardAvatar(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	png := mediaCharaPNG(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Seraphina"}}`)
	sc, err := build.NewCardStore().ImportBytes(png)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	defer srv.Close()

	// Gated: no token → 401, not a leak of the bytes.
	if resp := authedGet(t, srv.URL+"/media/cards/"+sc.ID, ""); resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("unauthenticated media: status %d, want 401", resp.StatusCode)
	}

	// Authenticated → 200, image/png, the ORIGINAL bytes.
	resp := authedGet(t, srv.URL+"/media/cards/"+sc.ID, "secret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated media: status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("cache-control = %q, want private (behind the gate)", cc)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, png) {
		t.Error("served avatar differs from the imported png bytes")
	}
}

// TestMediaServesBackground — the same gated media route serves scene backdrops.
func TestMediaServesBackground(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	png := append([]byte("\x89PNG\r\n\x1a\n"), 0, 1, 2, 3)
	bg, err := build.NewBackgroundStore().ImportBytes(png)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	defer srv.Close()

	if resp := authedGet(t, srv.URL+"/media/backgrounds/"+bg.ID, ""); resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("unauthenticated background: status %d, want 401", resp.StatusCode)
	}
	resp := authedGet(t, srv.URL+"/media/backgrounds/"+bg.ID, "secret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated background: status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("content-type = %q, want image/png", ct)
	}
}

func TestMediaMisses(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	defer srv.Close()

	for _, path := range []string{
		"/media/cards/does-not-exist-000000000000", // unknown id
		"/media/backgrounds/anything",              // unknown kind (until 2e)
		"/media/cards/",                            // no id
		"/media/",                                  // no kind
	} {
		resp := authedGet(t, srv.URL+path, "secret")
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, resp.StatusCode)
		}
	}
}
