package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"filippo.io/age"
)

// The shared-file envelope: how a file OWNED BY SOMEONE ELSE keeps secrets that
// terva can also open.
//
// auth.json and secrets.json are sealed whole, which is free because nothing
// but terva reads them. A component's own file is different on the one axis
// that matters: it has a second party. Sealing it whole would hand terva the
// component's ENTIRE state — a connector's chat ids, message history, who talks
// to whom — when all terva needs is the credentials. Per-value sealing bounds
// the co-recipient to exactly what was marked, and leaves everything else as
// structure passing through.
//
// So the rules, from docs/proposals/secrets-at-rest.md §8.3–8.5:
//
//   - JSON only. One format means one parser, no comments to lose, and Go
//     marshals object keys sorted so a rewrite is deterministic. Allowing three
//     formats was the objection that argued for whole-file, and mandating one
//     dissolves it rather than answering it.
//   - Each secret is sealed in place, as an "enc:age:v2:" string BOUND to its
//     own path, so a value moved elsewhere in the file will not open there.
//   - The paths are DECLARED. This is the only thing a schema is needed for:
//     scanning for the marker proves "these values are sealed", never "nothing
//     else should have been". Declared paths are what let a file be called
//     CLEAN — and a file with no declaration is not clean, it is unknown.
//
// Paths are RFC 6901 JSON Pointers ("/auth/bot_token"), which is what a
// component's manifest declares and what a mismatch error prints back.

// Envelope describes one component file: which paths hold secrets, and the
// scope those secrets are bound to.
type Envelope struct {
	// Scope names the owner in a binding — "conn:discord-ext", "ext:memory".
	Scope string
	// Paths are JSON Pointers to the values that must be sealed. A path that
	// is absent from a document is not an error: a connector that has not been
	// configured yet has no token, and that is a normal state rather than a
	// broken file.
	Paths []string
}

// ErrUndeclared reports a document that carries no declaration at all, which is
// UNKNOWN rather than clean.
var ErrUndeclared = errors.New("secrets: no declared secret paths for this file")

// Seal encrypts every declared path that is still plaintext, to all of
// recipients — the component's own key AND terva's, which is what lets terva
// rotate or audit while the component is stopped, disabled, or uninstalled.
//
// A value already sealed is left exactly as it is. Re-sealing on every write
// would churn the file and, worse, would quietly re-bind a value that had been
// MOVED there.
func (e Envelope) Seal(doc []byte, recipients ...age.Recipient) ([]byte, error) {
	if len(e.Paths) == 0 {
		return nil, ErrUndeclared
	}
	if len(recipients) == 0 {
		return nil, errors.New("secrets: seal with no recipients")
	}
	root, err := decode(doc)
	if err != nil {
		return nil, err
	}
	for _, p := range e.Paths {
		cur, ok, err := pointerGet(root, p)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // not configured yet
		}
		s, isString := cur.(string)
		if !isString {
			return nil, fmt.Errorf("secrets: %s holds a %T, and only a JSON string can be sealed", p, cur)
		}
		if s == "" || IsEncryptedField(s) {
			continue
		}
		sealed, err := EncodeField(Binding(e.Scope, p), s, recipients...)
		if err != nil {
			return nil, err
		}
		if err := pointerSet(root, p, sealed); err != nil {
			return nil, err
		}
	}
	return encode(root)
}

// Open decrypts every declared path, returning a document with the plaintext
// back in place. For the OWNER, at the point it needs its own credentials.
//
// A value that is not sealed passes through: a file written before the
// component learned to seal is still usable, and the next Seal fixes it.
func (e Envelope) Open(doc []byte, id age.Identity) ([]byte, error) {
	if len(e.Paths) == 0 {
		return nil, ErrUndeclared
	}
	root, err := decode(doc)
	if err != nil {
		return nil, err
	}
	for _, p := range e.Paths {
		cur, ok, err := pointerGet(root, p)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		s, isString := cur.(string)
		if !isString || !IsEncryptedField(s) {
			continue
		}
		plain, err := DecodeField(id, Binding(e.Scope, p), s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if err := pointerSet(root, p, plain); err != nil {
			return nil, err
		}
	}
	return encode(root)
}

// Clean reports whether every declared path in doc is sealed, naming the first
// that is not.
//
// This is what a deny-list lift can be built on, and the reason declared paths
// exist at all: scanning for the marker can only prove that what IS sealed is
// sealed. Only a declaration can say what SHOULD have been.
func (e Envelope) Clean(doc []byte) error {
	if len(e.Paths) == 0 {
		return ErrUndeclared
	}
	root, err := decode(doc)
	if err != nil {
		return err
	}
	for _, p := range e.Paths {
		cur, ok, err := pointerGet(root, p)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		s, isString := cur.(string)
		if !isString {
			return fmt.Errorf("%s holds a %T, which no declaration can vouch for", p, cur)
		}
		if s == "" {
			continue
		}
		if !IsEncryptedField(s) {
			return fmt.Errorf("%s is plaintext", p)
		}
	}
	return nil
}

// decode parses a document, preserving numbers exactly. A connector's poll
// offset is an int64 that would otherwise round-trip through float64 and come
// back wrong — silently, and only for large values.
func decode(doc []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(doc)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("secrets: envelope document is not a JSON object: %w", err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func encode(root map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// pointerGet resolves an RFC 6901 pointer. The third return distinguishes
// "absent" from "present and empty", which matters: absent is a component that
// has not been configured, and that is not a problem to report.
func pointerGet(root map[string]any, pointer string) (any, bool, error) {
	tokens, err := pointerTokens(pointer)
	if err != nil {
		return nil, false, err
	}
	var cur any = root
	for _, tok := range tokens {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return nil, false, nil
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false, nil
			}
			cur = node[i]
		default:
			return nil, false, nil
		}
	}
	return cur, true, nil
}

// pointerSet writes through a pointer whose parents already exist — Seal and
// Open only ever rewrite a value they just read.
func pointerSet(root map[string]any, pointer string, value string) error {
	tokens, err := pointerTokens(pointer)
	if err != nil {
		return err
	}
	var cur any = root
	for _, tok := range tokens[:len(tokens)-1] {
		switch node := cur.(type) {
		case map[string]any:
			cur = node[tok]
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(node) {
				return fmt.Errorf("secrets: %s does not resolve", pointer)
			}
			cur = node[i]
		default:
			return fmt.Errorf("secrets: %s does not resolve", pointer)
		}
	}
	last := tokens[len(tokens)-1]
	switch node := cur.(type) {
	case map[string]any:
		node[last] = value
		return nil
	case []any:
		i, err := strconv.Atoi(last)
		if err != nil || i < 0 || i >= len(node) {
			return fmt.Errorf("secrets: %s does not resolve", pointer)
		}
		node[i] = value
		return nil
	default:
		return fmt.Errorf("secrets: %s does not resolve", pointer)
	}
}

// pointerTokens splits and unescapes a JSON Pointer. ~1 is "/" and ~0 is "~",
// and the order matters: unescaping ~0 first would turn "~01" into "~1" and
// then into "/".
func pointerTokens(pointer string) ([]string, error) {
	if pointer == "" || !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("secrets: %q is not a JSON Pointer (it must start with /)", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		out = append(out, p)
	}
	return out, nil
}
