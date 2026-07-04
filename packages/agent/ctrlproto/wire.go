package ctrlproto

import "encoding/json"

// Kind discriminates a [Frame]. Framing follows the connproto philosophy:
// id-correlated commands and their responses, plus uncorrelated streamed
// events, all carrying session addressing in the envelope.
type Kind string

const (
	// KindHello is the capability handshake, sent once per side at connect.
	KindHello Kind = "hello"
	// KindCmd is a client→server command carrying an id and a method.
	KindCmd Kind = "cmd"
	// KindResp is the server→client response correlated to a command id.
	KindResp Kind = "resp"
	// KindEvent is a server→client streamed event, uncorrelated, addressed to
	// a session.
	KindEvent Kind = "event"
)

// Frame is one ctrlproto message. It is a discriminated union over [Kind]:
// only the fields relevant to the kind are populated. Addressing (Sess) rides
// the envelope from day one so the mobile-subset client and a future fleet
// controller need no reframe.
type Frame struct {
	Kind Kind `json:"kind"`
	// ID correlates a KindCmd with its KindResp. Zero for events and hellos.
	ID uint64 `json:"id,omitempty"`
	// Sess is the session this frame addresses (commands and events). Empty
	// means the workspace's current/default session.
	Sess string `json:"sess,omitempty"`

	// Hello is set on a KindHello frame.
	Hello *Hello `json:"hello,omitempty"`

	// Method + Params are set on a KindCmd frame.
	Method Method          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`

	// Result or Error is set on a KindResp frame (exactly one).
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`

	// Event is set on a KindEvent frame.
	Event *Event `json:"event,omitempty"`
}

// Error is a structured command failure returned in a [KindResp] frame.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Error codes. These are stable wire strings, not localized text.
const (
	CodeBusy         = "busy"         // a turn is already running on the session
	CodeNoSession    = "no_session"   // the addressed session does not exist
	CodeNotFound     = "not_found"    // a named resource does not exist
	CodeBadRequest   = "bad_request"  // malformed params
	CodeUnsupported  = "unsupported"  // method not served (e.g. an unimplemented control call)
	CodeUnauthorized = "unauthorized" // the auth gate rejected the caller
	CodeInternal     = "internal"     // an unexpected server-side failure
)

// Encode serializes a frame to its compact JSON wire bytes (no trailing
// newline; stream carriers add their own framing, WS carriers use the message
// boundary).
func Encode(f Frame) ([]byte, error) { return json.Marshal(f) }

// Decode parses frame bytes.
func Decode(b []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// Bind unmarshals a command frame's params into v. A frame with no params is a
// no-op (v is left at its zero value), so callers can Bind optional params
// safely.
func (f Frame) Bind(v any) error {
	if len(f.Params) == 0 {
		return nil
	}
	return json.Unmarshal(f.Params, v)
}

// BindResult unmarshals a response frame's result into v.
func (f Frame) BindResult(v any) error {
	if len(f.Result) == 0 {
		return nil
	}
	return json.Unmarshal(f.Result, v)
}

// HelloFrame builds a [KindHello] frame.
func HelloFrame(h Hello) Frame { return Frame{Kind: KindHello, Hello: &h} }

// CmdFrame builds a [KindCmd] frame, marshaling params (which may be nil).
func CmdFrame(id uint64, sess string, method Method, params any) (Frame, error) {
	raw, err := marshalOptional(params)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Kind: KindCmd, ID: id, Sess: sess, Method: method, Params: raw}, nil
}

// OKFrame builds a successful [KindResp] frame, marshaling result (nil result
// yields a bare ok response).
func OKFrame(id uint64, result any) (Frame, error) {
	raw, err := marshalOptional(result)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Kind: KindResp, ID: id, Result: raw}, nil
}

// ErrFrame builds a failed [KindResp] frame.
func ErrFrame(id uint64, code, msg string) Frame {
	return Frame{Kind: KindResp, ID: id, Error: &Error{Code: code, Message: msg}}
}

// EventFrame builds a [KindEvent] frame addressed to sess.
func EventFrame(sess string, ev Event) Frame {
	return Frame{Kind: KindEvent, Sess: sess, Event: &ev}
}

func marshalOptional(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
