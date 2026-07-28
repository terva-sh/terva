//go:build terva_web

package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/i18n"
)

// uploadPath is the attachment staging route. Mounted under authMiddleware, and
// deliberately not under /media/, which is a strictly read-only server for the
// content stores — the two directions do not belong on one prefix.
const uploadPath = "/upload"

// uploadField is the multipart part carrying the bytes.
const uploadField = "file"

// uploadHandler stages one file into store and answers with the id the client
// will name in its next prompt.
//
// Nothing about the file's description is taken from the request. The declared
// content type is ignored and the filename only ever suggests a label — the
// store decides what lands on disk, and the daemon re-derives type and size from
// what it actually wrote. So the worst a lying client achieves is a confusingly
// named attachment of its own.
//
// The response carries no path. A client has no use for one (it names the id,
// and the daemon resolves it), and handing out absolute host paths would make
// the browser a place from which to learn the filesystem layout.
//
// The store and the bound are arguments rather than package state because the
// composition root already owns this decision — it is the same value newMux
// hands the hello frame as MaxAttachmentBytes, and the route and the advertised
// limit disagreeing is precisely the failure a client cannot diagnose. Injecting
// it also lets the route's own size arithmetic be tested without pushing 100 MB
// through the handler on every CI run.
func uploadHandler(store *attach.Store, max int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !sameOrigin(r, "upload") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// A session id is required rather than defaulted: staging is per-session, and
		// a default would quietly pool one client's files where another's resolve.
		sess := r.URL.Query().Get("sess")
		if strings.TrimSpace(sess) == "" {
			writeUploadError(w, http.StatusBadRequest, i18n.T("upload requires a session"))
			return
		}
		// Bound the request body before touching it, so an oversized upload is
		// refused by the reader rather than by memory pressure. The slack over max
		// covers the multipart envelope (boundaries and part headers) around a file
		// that is itself exactly at the limit; the store still holds the real bound
		// on the bytes it writes.
		r.Body = http.MaxBytesReader(w, r.Body, max+uploadEnvelopeSlack)

		part, err := filePart(r)
		if err != nil {
			if isBodyTooLarge(err) {
				writeUploadError(w, http.StatusRequestEntityTooLarge, tooLargeMessage(max))
				return
			}
			writeUploadError(w, http.StatusBadRequest, i18n.T("upload is missing a file"))
			return
		}
		defer part.Close()

		ref, err := store.Stage(sess, part.FileName(), part)
		switch {
		case errors.Is(err, attach.ErrTooLarge) || isBodyTooLarge(err):
			writeUploadError(w, http.StatusRequestEntityTooLarge, tooLargeMessage(max))
			return
		case err != nil:
			// The client cannot act on a disk error, but an operator can, so it goes
			// to the daemon log in full and to the browser as a generic failure.
			fmt.Fprintf(os.Stderr, "terva web: attachment staging failed: %v\n", err)
			writeUploadError(w, http.StatusInternalServerError, i18n.T("could not save the attachment"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   ref.ID,
			"name": ref.Name,
			"mime": ref.Mime,
			"kind": ref.Kind,
			"size": ref.Size,
		})
	}
}

// uploadEnvelopeSlack is the multipart framing allowance on top of the file
// itself: two boundary lines and the part's headers, of which the filename is
// the only unbounded piece and is itself capped by the client. 8 KiB is far more
// than any real envelope and far less than a meaningful attack budget.
//
// It is what makes a file of exactly the limit succeed instead of 413ing on its
// own framing, which is an arithmetic claim and therefore one a test makes
// rather than a comment (upload_test.go).
const uploadEnvelopeSlack = 8 << 10

// filePart returns the upload's file part as a stream.
//
// It walks the multipart reader rather than calling r.FormFile, because FormFile
// materializes the whole part first — in memory up to its threshold, then into a
// file under os.TempDir. At a 100 MB limit that means writing every upload
// twice, and leaving the first copy somewhere this package does not own or
// sweep. NextPart hands back a reader that streams straight into the store.
//
// Non-file parts before the file are skipped so a client may send ordinary
// fields alongside it.
func filePart(r *http.Request) (*multipart.Part, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errNoFilePart
			}
			return nil, err
		}
		if part.FormName() == uploadField && part.FileName() != "" {
			return part, nil
		}
		_ = part.Close()
	}
}

// errNoFilePart reports a well-formed multipart body with no file in it.
var errNoFilePart = errors.New("no file part in the upload")

// isBodyTooLarge reports the MaxBytesReader overflow. net/http returns an
// unexported *http.MaxBytesError, so errors.As on the exported type is the
// supported check; the string fallback covers the wrapping multipart does.
func isBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "request body too large")
}

func tooLargeMessage(max int64) string {
	return i18n.T("attachment is too large (limit %d MB)", max>>20)
}

// writeUploadError answers with a JSON body, because the caller is fetch() and
// a bare text/plain error would surface in the panel as an opaque status code.
func writeUploadError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
