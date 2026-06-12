// Package connproto defines the JSON-over-stdin/stdout wire format
// spoken between zot and an external (out-of-process) chat connector.
// Both the host proxy (packages/agent/chat/external) and the author
// SDK (packages/agent/connsdk) marshal/unmarshal these types.
//
// This is deliberately a SEPARATE protocol from the extension one
// (packages/agent/extproto): connectors need a continuous child→host
// message stream, which the extension protocol lacks. The framing
// conventions are shared — one JSON object per LF-terminated line, a
// top-level Type discriminator, and an ID only on commands and their
// responses so the sender can correlate.
//
// Direction conventions in this file:
//   - Type names ending in "FromConn" are sent by the connector to zot.
//   - Type names ending in "FromHost" are sent by zot to the connector.
//   - Names without a suffix are shared value types.
//
// Versioning is negotiated, not announce-only: the connector's hello
// carries [ProtocolMin, ProtocolMax] and the host refuses the spawn
// with a clear error when its own version falls outside that range.
// The golden-frame tests in connproto_test.go pin this schema; do not
// change field names or types without bumping ProtocolVersion.
package connproto

import "encoding/json"

// ProtocolVersion is the host's wire-format version.
const ProtocolVersion = 1

// Frame is the minimal envelope every line decodes into first.
type Frame struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// Capabilities describes the service's formatting limits and upload
// support, declared once in hello. Mirrors chat.Capabilities with
// wire-friendly types (milliseconds instead of time.Duration).
type Capabilities struct {
	// MaxTextLen is the outbound chunking threshold in bytes; the
	// host splits longer replies. 0 = no limit.
	MaxTextLen int `json:"max_text_len,omitempty"`
	// TypingRefreshMS is how often the typing indicator must be
	// re-asserted to stay visible; 0 = service has no indicator.
	TypingRefreshMS int  `json:"typing_refresh_ms,omitempty"`
	SendsImages     bool `json:"sends_images,omitempty"`
	SendsFiles      bool `json:"sends_files,omitempty"`
}

// Attachment is one inbound file, passed by path (same-host
// assumption, like extensions): the connector writes the bytes under
// its host-assigned data_dir and the host reads + deletes the file.
// Keeps frames far below the 4MiB line cap.
type Attachment struct {
	MimeType string `json:"mime_type"`
	Path     string `json:"path"`
}

// ---- connector → host ----

// HelloFromConn is the first frame on the connector's stdout.
type HelloFromConn struct {
	Type         string       `json:"type"` // "hello"
	Name         string       `json:"name"`
	Version      string       `json:"version,omitempty"`
	ProtocolMin  int          `json:"protocol_min"`
	ProtocolMax  int          `json:"protocol_max"`
	Capabilities Capabilities `json:"capabilities"`
}

// ConnectedFromConn answers ConnectFromHost on success with the
// bot's own identity on the service.
type ConnectedFromConn struct {
	Type     string `json:"type"` // "connected"
	ID       string `json:"id,omitempty"`
	Username string `json:"username,omitempty"`
}

// ConnectErrorFromConn answers ConnectFromHost when credentials are
// permanently broken (bad token), not a transient blip — transient
// failures are the connector's job to retry internally.
type ConnectErrorFromConn struct {
	Type  string `json:"type"` // "connect_error"
	Error string `json:"error"`
}

// MessageFromConn is one normalized inbound chat message.
type MessageFromConn struct {
	Type        string       `json:"type"` // "message"
	ChatID      string       `json:"chat_id"`
	UserID      string       `json:"user_id"`
	Username    string       `json:"username,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Text        string       `json:"text,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// ResultFromConn acknowledges one send/send_image/send_file command.
type ResultFromConn struct {
	Type  string `json:"type"` // "result"
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

// WarnFromConn surfaces an operational log line ("gateway
// reconnecting") without ending the session.
type WarnFromConn struct {
	Type    string `json:"type"` // "warn"
	Message string `json:"message"`
}

// ---- host → connector ----

// HelloAckFromHost answers hello with the negotiated protocol version
// and the connector's scratch directory for inbound attachments.
// ZotVersion and TervaVersion carry the SAME value under both naming
// eras (the rename bridge, docs/plans/rename-terva.md); zot_version
// stays until the connector-SDK deprecation window closes.
type HelloAckFromHost struct {
	Type         string `json:"type"` // "hello_ack"
	Protocol     int    `json:"protocol"`
	ZotVersion   string `json:"zot_version,omitempty"`
	TervaVersion string `json:"terva_version,omitempty"`
	DataDir      string `json:"data_dir,omitempty"`
}

// ConnectFromHost asks the connector to establish its service session
// and answer with connected or connect_error.
type ConnectFromHost struct {
	Type string `json:"type"` // "connect"
}

// SendFromHost delivers one outbound text message (pre-chunked by the
// host to the declared max_text_len).
type SendFromHost struct {
	Type    string `json:"type"` // "send"
	ID      string `json:"id"`
	ChatID  string `json:"chat_id"`
	ReplyTo string `json:"reply_to,omitempty"`
	Text    string `json:"text"`
}

// SendImageFromHost uploads a local file as an inline image.
type SendImageFromHost struct {
	Type    string `json:"type"` // "send_image"
	ID      string `json:"id"`
	ChatID  string `json:"chat_id"`
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

// SendFileFromHost uploads a local file as a raw document.
type SendFileFromHost struct {
	Type    string `json:"type"` // "send_file"
	ID      string `json:"id"`
	ChatID  string `json:"chat_id"`
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

// TypingFromHost asserts the typing indicator once. Fire-and-forget:
// no ID, no result.
type TypingFromHost struct {
	Type   string `json:"type"` // "typing"
	ChatID string `json:"chat_id"`
}

// ShutdownFromHost asks the connector to exit; the host closes stdin
// right after and escalates to SIGTERM/SIGKILL on a deadline.
type ShutdownFromHost struct {
	Type string `json:"type"` // "shutdown"
}

// Encode marshals v and appends the LF frame terminator.
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
