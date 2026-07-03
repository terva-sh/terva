package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/connsdk"
)

// fakeAPI is a scriptable seam so transport logic runs with zero
// network and zero SDK.
type fakeAPI struct {
	mu            sync.Mutex
	meErr         error
	appIDErr      error // when set, appID fails (invite URL falls back to the bot id)
	onMessage     func(inboundMessage)
	onInteraction func(inboundInteraction)
	onMembership  func(inboundMembership)
	onChatEvent   func(inboundChatEvent)
	edits         []string // chatID|messageID|text
	deletes       []string // chatID|messageID
	reacts        []string // chatID|messageID|emoji|remove
	sends         []string // "chatID|replyTo|text"
	files         []string
	typings       []string
	asks          []string // "chatID|replyTo|askID|text|key:style,key:style"
	askUpdates    []string // "chatID|messageID|content"
	acks          []string // interaction ids acknowledged
	ephemerals    []string // "interactionID|text"
	webhookErr    error    // when set, sendAsWebhook fails (DM / no MANAGE_WEBHOOKS)
	webhookSends  []string // "chatID|name|text"
	threads       []string // "chatID|fromMessageID|name"
}

func (f *fakeAPI) me(ctx context.Context) (string, string, error) {
	if f.meErr != nil {
		return "", "", f.meErr
	}
	return "bot-9", "tervabot", nil
}

func (f *fakeAPI) appID(ctx context.Context) (string, error) {
	if f.appIDErr != nil {
		return "", f.appIDErr
	}
	return "app-42", nil
}

func (f *fakeAPI) open(ctx context.Context, onMessage func(inboundMessage), onInteraction func(inboundInteraction), onMembership func(inboundMembership), onChatEvent func(inboundChatEvent)) error {
	f.mu.Lock()
	f.onMessage = onMessage
	f.onInteraction = onInteraction
	f.onMembership = onMembership
	f.onChatEvent = onChatEvent
	f.mu.Unlock()
	return nil
}

func (f *fakeAPI) event(ev inboundChatEvent) {
	f.mu.Lock()
	h := f.onChatEvent
	f.mu.Unlock()
	if h != nil {
		h(ev)
	}
}

func (f *fakeAPI) editMessage(ctx context.Context, channelID, messageID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, channelID+"|"+messageID+"|"+text)
	return nil
}

func (f *fakeAPI) deleteMessage(ctx context.Context, channelID, messageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, channelID+"|"+messageID)
	return nil
}

func (f *fakeAPI) react(ctx context.Context, channelID, messageID, emoji string, remove bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reacts = append(f.reacts, fmt.Sprintf("%s|%s|%s|%v", channelID, messageID, emoji, remove))
	return nil
}

func (f *fakeAPI) join(mb inboundMembership) {
	f.mu.Lock()
	h := f.onMembership
	f.mu.Unlock()
	if h != nil {
		h(mb)
	}
}

func (f *fakeAPI) close() {}

func (f *fakeAPI) deliver(im inboundMessage) {
	f.mu.Lock()
	h := f.onMessage
	f.mu.Unlock()
	if h != nil {
		h(im)
	}
}

func (f *fakeAPI) click(ii inboundInteraction) {
	f.mu.Lock()
	h := f.onInteraction
	f.mu.Unlock()
	if h != nil {
		h(ii)
	}
}

func (f *fakeAPI) sendAsk(ctx context.Context, channelID, replyTo, text, askID string, buttons []askButton) (string, error) {
	var bs []string
	for _, b := range buttons {
		bs = append(bs, b.Key+":"+b.Style)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks = append(f.asks, channelID+"|"+replyTo+"|"+askID+"|"+text+"|"+strings.Join(bs, ","))
	return fmt.Sprintf("ask-msg-%d", len(f.asks)), nil
}

func (f *fakeAPI) updateAskMessage(ctx context.Context, channelID, messageID, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.askUpdates = append(f.askUpdates, channelID+"|"+messageID+"|"+content)
	return nil
}

func (f *fakeAPI) ackComponent(ctx context.Context, interactionID, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, interactionID)
	return nil
}

func (f *fakeAPI) respondEphemeral(ctx context.Context, interactionID, token, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ephemerals = append(f.ephemerals, interactionID+"|"+text)
	return nil
}

func (f *fakeAPI) sendAsWebhook(ctx context.Context, channelID, name, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.webhookErr != nil {
		return "", f.webhookErr
	}
	f.webhookSends = append(f.webhookSends, channelID+"|"+name+"|"+text)
	return fmt.Sprintf("cast-%d", len(f.webhookSends)), nil
}

func (f *fakeAPI) createThread(ctx context.Context, channelID, fromMessageID, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads = append(f.threads, channelID+"|"+fromMessageID+"|"+name)
	return fmt.Sprintf("thread-%d", len(f.threads)), nil
}

func (f *fakeAPI) sendMessage(ctx context.Context, channelID, replyTo, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, channelID+"|"+replyTo+"|"+text)
	return fmt.Sprintf("sent-%d", len(f.sends)), nil
}

func (f *fakeAPI) sendFile(ctx context.Context, channelID, path, name, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = append(f.files, channelID+"|"+name+"|"+caption)
	return nil
}

func (f *fakeAPI) typing(ctx context.Context, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typings = append(f.typings, channelID)
	return nil
}

// withFakeAPI swaps the seam factory for one test.
func withFakeAPI(t *testing.T, f *fakeAPI) {
	t.Helper()
	prev := newAPI
	newAPI = func(token string) (api, error) { return f, nil }
	t.Cleanup(func() { newAPI = prev })
}

func connectedTransport(t *testing.T, f *fakeAPI) (*Transport, chan connsdk.Message, context.CancelFunc) {
	t.Helper()
	withFakeAPI(t, f)
	tr, err := NewTransport("tok", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	got := make(chan connsdk.Message, 8)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = tr.Receive(ctx, func(m connsdk.Message) { got <- m }) }()
	// Receive registers the handler via open; wait for it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		ready := f.onMessage != nil
		f.mu.Unlock()
		if ready {
			return tr, got, cancel
		}
		if time.Now().After(deadline) {
			t.Fatal("Receive never opened the gateway")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConnectIdentity(t *testing.T) {
	f := &fakeAPI{}
	withFakeAPI(t, f)
	tr, err := NewTransport("tok", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := tr.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if id.ID != "bot-9" || id.Username != "tervabot" {
		t.Errorf("identity = %+v", id)
	}

	f2 := &fakeAPI{meErr: errors.New("401")}
	withFakeAPI(t, f2)
	tr2, _ := NewTransport("bad", t.TempDir())
	if _, err := tr2.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("bad-token Connect = %v, want rejection", err)
	}
}

func TestNoTokenRefused(t *testing.T) {
	if _, err := NewTransport("", t.TempDir()); err == nil {
		t.Error("empty token must be refused at construction")
	}
}

// TestNormalizePosture pins the delivery rules: real user messages
// flow; the bot's own, other bots', webhooks', and content-stripped
// guild messages (the MESSAGE_CONTENT gate ⇒ mention-only groups) do
// not. Stage A semantics are pinned: ID is the message's own id,
// ReplyTo the true in-reply-to, chat kind from the guild presence.
func TestNormalizePosture(t *testing.T) {
	f := &fakeAPI{}
	_, got, _ := connectedTransport(t, f)

	f.deliver(inboundMessage{MessageID: "m1", ChannelID: "c1", AuthorID: "u1", AuthorName: "drew", Content: "hi"})
	f.deliver(inboundMessage{MessageID: "m2", ChannelID: "c1", AuthorID: "bot-9", AuthorName: "tervabot", Content: "self"})
	f.deliver(inboundMessage{MessageID: "m3", ChannelID: "c1", AuthorID: "u2", AuthorIsBot: true, Content: "other bot"})
	f.deliver(inboundMessage{MessageID: "m4", ChannelID: "c1", GuildID: "g1", AuthorID: "u1", Content: ""}) // stripped
	f.deliver(inboundMessage{MessageID: "m5", ChannelID: "c1", AuthorID: "u1", AuthorName: "drew", Content: "bye"})

	want := []string{"hi", "bye"}
	for i, w := range want {
		select {
		case m := <-got:
			if m.Text != w {
				t.Errorf("message %d = %q, want %q", i, m.Text, w)
			}
			if w == "hi" && (m.ChatID != "c1" || m.UserID != "u1" || m.ID != "m1" || m.ChatKind != "dm") {
				t.Errorf("normalization = %+v", m)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("message %d never arrived", i)
		}
	}
	select {
	case m := <-got:
		t.Errorf("unexpected extra message: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestAttachmentIngest: attachments download into the data dir and
// ride by path — images as kind image, everything else labeled with
// its stage-E kind (this test predates stage E; it pinned
// images-only until D5 widened the flow).
func TestAttachmentIngest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PNGBYTES"))
	}))
	defer srv.Close()

	f := &fakeAPI{}
	withFakeAPI(t, f)
	dataDir := t.TempDir()
	tr, err := NewTransport("tok", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, ok := tr.normalize(inboundMessage{
		MessageID: "m1", ChannelID: "c1", AuthorID: "u1", Content: "look",
		Attachments: []inboundAttachment{
			{URL: srv.URL + "/pic.png", Filename: "pic.png", ContentType: "image/png"},
			{URL: srv.URL + "/doc.pdf", Filename: "doc.pdf", ContentType: "application/pdf"},
		},
	})
	if !ok {
		t.Fatal("message should deliver")
	}
	if len(m.Attachments) != 2 || m.Attachments[0].MimeType != "image/png" || m.Attachments[0].Kind != "image" {
		t.Fatalf("attachments = %+v, want the image first", m.Attachments)
	}
	if m.Attachments[1].Kind != "document" {
		t.Fatalf("pdf attachment = %+v, want kind document", m.Attachments[1])
	}
	b, err := os.ReadFile(m.Attachments[0].Path)
	if err != nil || string(b) != "PNGBYTES" {
		t.Errorf("downloaded bytes = %q err=%v", b, err)
	}
	if !strings.HasPrefix(m.Attachments[0].Path, dataDir+string(filepath.Separator)) {
		t.Errorf("attachment %q escaped the data dir", m.Attachments[0].Path)
	}
}

func TestOutbound(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)

	if err := tr.Send(context.Background(), connsdk.Outgoing{ChatID: "c1", ReplyTo: "m7", Text: "pong"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Typing(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(img, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.SendImage(context.Background(), "c1", img, "a plot"); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != 1 || f.sends[0] != "c1|m7|pong" {
		t.Errorf("sends = %v", f.sends)
	}
	f.mu.Unlock()
	// The protocol-2 id-returning path (connsdk.MessageIDSender).
	if id, err := tr.SendWithID(context.Background(), connsdk.Outgoing{ChatID: "c1", Text: "again"}); err != nil || id != "sent-2" {
		t.Errorf("SendWithID = %q err=%v", id, err)
	}
	f.mu.Lock()
	if len(f.typings) != 1 || f.typings[0] != "c1" {
		t.Errorf("typings = %v", f.typings)
	}
	if len(f.files) != 1 || f.files[0] != "c1|x.png|a plot" {
		t.Errorf("files = %v", f.files)
	}
}

// TestAskButtons drives the D2 surface: the ask renders buttons with
// styled custom_ids; a stale click gets an ephemeral notice; a
// disallowed clicker gets the ephemeral rejection; the owner's click
// acks and delivers an ATTESTED answer; CloseAsk strips the buttons
// and renders the outcome.
func TestAskButtons(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)

	answers := make(chan connsdk.Answer, 4)
	mid, err := tr.Ask(context.Background(), connsdk.Ask{
		ID: "a1", ChatID: "c1", ReplyTo: "m12", Text: "approve?",
		Options: []connsdk.AskOption{
			{Key: "approve", Label: "Approve", Style: "affirm"},
			{Key: "deny", Label: "Deny", Style: "deny"},
		},
		RestrictTo: []string{"u1"},
	}, func(a connsdk.Answer) { answers <- a })
	if err != nil || mid != "ask-msg-1" {
		t.Fatalf("Ask = %q, %v", mid, err)
	}
	f.mu.Lock()
	if len(f.asks) != 1 || f.asks[0] != "c1|m12|a1|approve?|approve:affirm,deny:deny" {
		t.Errorf("asks = %v", f.asks)
	}
	f.mu.Unlock()

	// A click on some other message's component is not ours: ignored.
	f.click(inboundInteraction{InteractionID: "i0", CustomID: "unrelated-widget", UserID: "u1"})
	// A click on a closed/unknown ask: ephemeral notice.
	f.click(inboundInteraction{InteractionID: "i1", CustomID: "ask:zombie:approve", UserID: "u1"})
	// The wrong user: ephemeral rejection, nothing delivered.
	f.click(inboundInteraction{InteractionID: "i2", CustomID: "ask:a1:approve", UserID: "intruder", Username: "mallory"})
	// The owner approves: silent ack + attested answer.
	f.click(inboundInteraction{InteractionID: "i3", CustomID: "ask:a1:approve", UserID: "u1", Username: "drew"})

	select {
	case a := <-answers:
		want := connsdk.Answer{AskID: "a1", Key: "approve", UserID: "u1", Username: "drew",
			Attestation: connsdk.AttestationAttested}
		if a != want {
			t.Errorf("answer = %+v, want %+v", a, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer never delivered")
	}
	select {
	case a := <-answers:
		t.Fatalf("extra answer delivered: %+v", a)
	default:
	}

	f.mu.Lock()
	if len(f.ephemerals) != 2 ||
		!strings.Contains(f.ephemerals[0], "i1|") || !strings.Contains(f.ephemerals[0], "no longer active") ||
		!strings.Contains(f.ephemerals[1], "i2|") || !strings.Contains(f.ephemerals[1], "isn't for you") {
		t.Errorf("ephemerals = %v", f.ephemerals)
	}
	if len(f.acks) != 1 || f.acks[0] != "i3" {
		t.Errorf("acks = %v", f.acks)
	}
	f.mu.Unlock()

	if err := tr.CloseAsk(context.Background(), "a1", "Approve — @drew"); err != nil {
		t.Fatalf("CloseAsk: %v", err)
	}
	f.mu.Lock()
	if len(f.askUpdates) != 1 || f.askUpdates[0] != "c1|ask-msg-1|approve?\n\n▸ Approve — @drew" {
		t.Errorf("askUpdates = %v", f.askUpdates)
	}
	f.mu.Unlock()

	// Closing again is a no-op (idempotent), and clicks after close go
	// to the stale path.
	if err := tr.CloseAsk(context.Background(), "a1", "whatever"); err != nil {
		t.Fatalf("second CloseAsk: %v", err)
	}
	f.click(inboundInteraction{InteractionID: "i4", CustomID: "ask:a1:deny", UserID: "u1"})
	f.mu.Lock()
	if len(f.askUpdates) != 1 {
		t.Errorf("askUpdates after idempotent close = %v", f.askUpdates)
	}
	if len(f.ephemerals) != 3 || !strings.Contains(f.ephemerals[2], "no longer active") {
		t.Errorf("post-close click ephemerals = %v", f.ephemerals)
	}
	f.mu.Unlock()
}

func TestParseAskCustomID(t *testing.T) {
	cases := []struct {
		in    string
		askID string
		key   string
		ok    bool
	}{
		{"ask:a1:approve", "a1", "approve", true},
		{"ask:143022000001-9:with:colons", "143022000001-9", "with:colons", true},
		{"ask:a1:", "", "", false},
		{"ask::x", "", "", false},
		{"ask:a1", "", "", false},
		{"poll:a1:x", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		askID, key, ok := parseAskCustomID(c.in)
		if askID != c.askID || key != c.key || ok != c.ok {
			t.Errorf("parseAskCustomID(%q) = %q,%q,%v want %q,%q,%v", c.in, askID, key, ok, c.askID, c.key, c.ok)
		}
	}
}

// TestAskFeatureDeclared pins the capability contract: the transport
// implements connsdk.Asker AND declares the matching feature string —
// the pair the host's Capabilities().Asks gate is built on.
func TestAskFeatureDeclared(t *testing.T) {
	var _ connsdk.Asker = (*Transport)(nil)
	found := false
	for _, f := range Capabilities().Features {
		if f == "asks" {
			found = true
		}
	}
	if !found {
		t.Error(`Capabilities().Features must declare "asks"`)
	}
}

// TestSpeakerSend drives D3: a speaker message rides the managed
// webhook under the sanitized display name (no reply threading), and
// a webhook failure (DM, missing MANAGE_WEBHOOKS) degrades to the
// prefixed plain send instead of losing the line.
func TestSpeakerSend(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)

	mid, err := tr.SendAsSpeaker(context.Background(), connsdk.Outgoing{
		ChatID: "c1", ReplyTo: "m7", Text: "The airlock hisses open.",
		Speaker: &connsdk.Speaker{Key: "kaiku", Name: "Kaiku"},
	})
	if err != nil || mid != "cast-1" {
		t.Fatalf("SendAsSpeaker = %q, %v", mid, err)
	}
	f.mu.Lock()
	if len(f.webhookSends) != 1 || f.webhookSends[0] != "c1|Kaiku|The airlock hisses open." {
		t.Errorf("webhookSends = %v", f.webhookSends)
	}
	if len(f.sends) != 0 {
		t.Errorf("plain sends = %v, want none on the happy path", f.sends)
	}
	f.mu.Unlock()

	// Webhook impossible → prefix fallback through the ordinary send.
	f.mu.Lock()
	f.webhookErr = errors.New("403 missing MANAGE_WEBHOOKS")
	f.mu.Unlock()
	mid, err = tr.SendAsSpeaker(context.Background(), connsdk.Outgoing{
		ChatID: "c2", Text: "hello",
		Speaker: &connsdk.Speaker{Key: "aava", Name: "Aava"},
	})
	if err != nil || mid != "sent-1" {
		t.Fatalf("fallback SendAsSpeaker = %q, %v", mid, err)
	}
	f.mu.Lock()
	if len(f.sends) != 1 || f.sends[0] != "c2||**Aava:** hello" {
		t.Errorf("fallback sends = %v", f.sends)
	}
	f.mu.Unlock()
}

func TestSanitizeSpeakerName(t *testing.T) {
	long := strings.Repeat("a", 100)
	cases := []struct{ in, want string }{
		{"Kaiku", "Kaiku"},
		{"", "cast"},
		{"Clyde the Bold", "C‍lyde the Bold"},
		{"my discord pal", "my d‍iscord pal"},
		{long, long[:80]},
	}
	for _, c := range cases {
		if got := sanitizeSpeakerName(c.in); got != c.want {
			t.Errorf("sanitizeSpeakerName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSpeakerFeatureDeclared pins the capability contract for D3, as
// TestAskFeatureDeclared does for D2.
func TestSpeakerFeatureDeclared(t *testing.T) {
	var _ connsdk.SpeakerSender = (*Transport)(nil)
	found := false
	for _, f := range Capabilities().Features {
		if f == "speaker:name_only" {
			found = true
		}
	}
	if !found {
		t.Error(`Capabilities().Features must declare "speaker:name_only"`)
	}
}

// TestStartThread drives D4: anchored and anchorless creation, the
// 100-char name cap, and the empty-name floor.
func TestStartThread(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)

	id, err := tr.StartThread(context.Background(), "c1", "m-12", "refactor: extract session core")
	if err != nil || id != "thread-1" {
		t.Fatalf("StartThread = %q, %v", id, err)
	}
	if _, err := tr.StartThread(context.Background(), "c1", "", strings.Repeat("n", 150)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.StartThread(context.Background(), "c1", "", "  "); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.threads) != 3 || f.threads[0] != "c1|m-12|refactor: extract session core" {
		t.Fatalf("threads = %v", f.threads)
	}
	if f.threads[1] != "c1||"+strings.Repeat("n", 100) {
		t.Errorf("long name not capped: %q", f.threads[1])
	}
	if f.threads[2] != "c1||terva" {
		t.Errorf("empty name floor = %q", f.threads[2])
	}
}

// TestThreadKindInbound: a message in a thread channel normalizes to
// chat kind "thread".
func TestThreadKindInbound(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)
	m, ok := tr.normalize(inboundMessage{
		MessageID: "m1", ChannelID: "t-99", GuildID: "g1", IsThread: true,
		AuthorID: "u1", Content: "inside the thread",
	})
	if !ok || m.ChatKind != "thread" || m.ChatID != "t-99" {
		t.Errorf("normalized = %+v ok=%v", m, ok)
	}
}

// TestThreadFeatureDeclared pins the capability contract for D4.
func TestThreadFeatureDeclared(t *testing.T) {
	var _ connsdk.Threader = (*Transport)(nil)
	found := false
	for _, f := range Capabilities().Features {
		if f == "threads_out" {
			found = true
		}
	}
	if !found {
		t.Error(`Capabilities().Features must declare "threads_out"`)
	}
}

// TestMentionEntities pins the stage-B mention signal: a located
// entity for the raw token, an unlocated one for reply-mentions, and
// none for other people's mentions.
func TestMentionEntities(t *testing.T) {
	f := &fakeAPI{}
	_, got, _ := connectedTransport(t, f)

	f.deliver(inboundMessage{MessageID: "m1", ChannelID: "c1", GuildID: "g1",
		AuthorID: "u1", Content: "hey <@bot-9> look", Mentions: []string{"bot-9"}})
	f.deliver(inboundMessage{MessageID: "m2", ChannelID: "c1", GuildID: "g1",
		AuthorID: "u1", Content: "reply ping", Mentions: []string{"bot-9"}})
	f.deliver(inboundMessage{MessageID: "m3", ChannelID: "c1", GuildID: "g1",
		AuthorID: "u1", Content: "for <@u2> only", Mentions: []string{"u2"}})

	m1 := <-got
	if len(m1.Entities) != 1 || m1.Entities[0].Kind != "bot_mention" ||
		m1.Entities[0].Offset != 4 || m1.Entities[0].Length != 8 {
		t.Errorf("located entity = %+v", m1.Entities)
	}
	m2 := <-got
	if len(m2.Entities) != 1 || m2.Entities[0].Offset != 0 || m2.Entities[0].Length != 0 {
		t.Errorf("unlocated entity = %+v", m2.Entities)
	}
	m3 := <-got
	if len(m3.Entities) != 0 {
		t.Errorf("foreign mention leaked an entity: %+v", m3.Entities)
	}
}

// TestGuildMembership: joins and leaves surface as admission events on
// the system channel; guilds without one are skipped.
func TestGuildMembership(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)

	events := make(chan connsdk.Membership, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.ReceiveMembership(ctx, func(mb connsdk.Membership) { events <- mb }) }()
	// Registration is async; poll until installed.
	deadline := time.Now().Add(2 * time.Second)
	for {
		tr.mu.Lock()
		ready := tr.membership != nil
		tr.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ReceiveMembership never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	f.join(inboundMembership{ChannelID: "sys-1", Title: "ops", Added: true})
	f.join(inboundMembership{ChannelID: "", Title: "no-sys", Added: true}) // skipped
	f.join(inboundMembership{ChannelID: "sys-1", Title: "ops", Added: false})

	select {
	case mb := <-events:
		want := connsdk.Membership{ChatID: "sys-1", ChatKind: "group", ChatTitle: "ops", Change: "added"}
		if mb != want {
			t.Errorf("added = %+v, want %+v", mb, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("added event never arrived")
	}
	select {
	case mb := <-events:
		if mb.Change != "removed" || mb.ChatID != "sys-1" {
			t.Errorf("removed = %+v", mb)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("removed event never arrived")
	}
	select {
	case mb := <-events:
		t.Errorf("no-system-channel guild leaked: %+v", mb)
	default:
	}
}

// TestStageBFeaturesDeclared pins the capability contract for the
// stage-B pair.
func TestStageBFeaturesDeclared(t *testing.T) {
	var _ connsdk.MembershipSource = (*Transport)(nil)
	want := map[string]bool{"entities": false, "chat_membership": false}
	for _, f := range Capabilities().Features {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, ok := range want {
		if !ok {
			t.Errorf("Capabilities().Features must declare %q", f)
		}
	}
}

// TestChatEvents drives D5's inbound side: edits (content-less
// partials dropped), deletions, and reactions (own toggles filtered).
func TestChatEvents(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)

	edited := make(chan connsdk.MessageEdited, 4)
	deleted := make(chan connsdk.MessageDeleted, 4)
	reactions := make(chan connsdk.Reaction, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = tr.ReceiveChatEvents(ctx, connsdk.ChatEventSink{
			Edited:   func(ev connsdk.MessageEdited) { edited <- ev },
			Deleted:  func(ev connsdk.MessageDeleted) { deleted <- ev },
			Reaction: func(ev connsdk.Reaction) { reactions <- ev },
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tr.mu.Lock()
		ready := tr.events != nil
		tr.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ReceiveChatEvents never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	f.event(inboundChatEvent{Kind: "edited", ChannelID: "c1", MessageID: "m10",
		TS: 5, Content: "fixed <@bot-9>", Mentions: []string{"bot-9"}})
	f.event(inboundChatEvent{Kind: "edited", ChannelID: "c1", MessageID: "m11", Content: ""}) // partial → dropped
	f.event(inboundChatEvent{Kind: "deleted", ChannelID: "c1", MessageID: "m12"})
	f.event(inboundChatEvent{Kind: "reaction_add", ChannelID: "c1", MessageID: "m-90",
		UserID: "u1", Username: "drew", Emoji: "👍"})
	f.event(inboundChatEvent{Kind: "reaction_add", ChannelID: "c1", MessageID: "m-90",
		UserID: "bot-9", Emoji: "👀"}) // own toggle → filtered
	f.event(inboundChatEvent{Kind: "reaction_remove", ChannelID: "c1", MessageID: "m-90",
		UserID: "u1", Emoji: "👍"})

	ed := <-edited
	if ed.ID != "m10" || ed.Text != "fixed <@bot-9>" || len(ed.Entities) != 1 {
		t.Errorf("edited = %+v", ed)
	}
	del := <-deleted
	if del.ID != "m12" {
		t.Errorf("deleted = %+v", del)
	}
	r1 := <-reactions
	if r1.Key != "👍" || r1.Removed || r1.Username != "drew" {
		t.Errorf("reaction add = %+v", r1)
	}
	r2 := <-reactions
	if !r2.Removed {
		t.Errorf("reaction remove = %+v", r2)
	}
	select {
	case r := <-reactions:
		t.Errorf("own reaction leaked: %+v", r)
	default:
	}
	select {
	case e := <-edited:
		t.Errorf("content-less edit leaked: %+v", e)
	default:
	}
}

// TestOutboundMessageOps drives D5's outbound side through the seam.
func TestOutboundMessageOps(t *testing.T) {
	f := &fakeAPI{}
	tr, _, _ := connectedTransport(t, f)

	if err := tr.EditMessage(context.Background(), "c1", "m-90", "v2"); err != nil {
		t.Fatal(err)
	}
	if err := tr.React(context.Background(), "c1", "m-12", "👀", false); err != nil {
		t.Fatal(err)
	}
	if err := tr.React(context.Background(), "c1", "m-12", "👀", true); err != nil {
		t.Fatal(err)
	}
	if err := tr.DeleteMessage(context.Background(), "c1", "m-90"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edits) != 1 || f.edits[0] != "c1|m-90|v2" {
		t.Errorf("edits = %v", f.edits)
	}
	if len(f.reacts) != 2 || f.reacts[0] != "c1|m-12|👀|false" || f.reacts[1] != "c1|m-12|👀|true" {
		t.Errorf("reacts = %v", f.reacts)
	}
	if len(f.deletes) != 1 || f.deletes[0] != "c1|m-90" {
		t.Errorf("deletes = %v", f.deletes)
	}
}

// TestAttachmentKindsIngest: every kind downloads with its label; the
// kind maps from the mime type.
func TestAttachmentKindsIngest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BYTES"))
	}))
	defer srv.Close()

	f := &fakeAPI{}
	withFakeAPI(t, f)
	tr, err := NewTransport("tok", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, ok := tr.normalize(inboundMessage{
		MessageID: "m1", ChannelID: "c1", AuthorID: "u1", Content: "files",
		Attachments: []inboundAttachment{
			{URL: srv.URL + "/v.ogg", Filename: "v.ogg", ContentType: "audio/ogg", Size: 5, DurationMS: 4200},
			{URL: srv.URL + "/d.pdf", Filename: "d.pdf", ContentType: "application/pdf", Size: 5},
		},
	})
	if !ok || len(m.Attachments) != 2 {
		t.Fatalf("attachments = %+v ok=%v", m.Attachments, ok)
	}
	if a := m.Attachments[0]; a.Kind != "audio" || a.Duration != 4200*time.Millisecond || a.Size != 5 {
		t.Errorf("audio attachment = %+v", a)
	}
	if a := m.Attachments[1]; a.Kind != "document" || a.Name != "d.pdf" {
		t.Errorf("document attachment = %+v", a)
	}
}

// TestStageDEFeaturesDeclared pins the capability contract for D5.
func TestStageDEFeaturesDeclared(t *testing.T) {
	var _ connsdk.ChatEventSource = (*Transport)(nil)
	var _ connsdk.MessageEditor = (*Transport)(nil)
	var _ connsdk.MessageReactor = (*Transport)(nil)
	var _ connsdk.MessageDeleter = (*Transport)(nil)
	want := map[string]bool{
		"edits_in": false, "deletes_in": false, "reactions_in": false,
		"edits_out": false, "reactions_out": false, "deletes_out": false,
		"attachment_kinds": false,
	}
	caps := Capabilities()
	for _, f := range caps.Features {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, ok := range want {
		if !ok {
			t.Errorf("Capabilities().Features must declare %q", f)
		}
	}
	if caps.MinEditInterval != time.Second {
		t.Errorf("MinEditInterval = %v, want 1s", caps.MinEditInterval)
	}
}

// TestInviteURL: setup's invite link carries the application id from
// the application-info endpoint when reachable, falls back to the bot
// user id otherwise (equal for every modern bot), and bakes the full
// permission set.
func TestInviteURL(t *testing.T) {
	got := inviteURL(resolveInviteAppID(context.Background(), &fakeAPI{}, "bot-9"))
	want := "https://discord.com/oauth2/authorize?client_id=app-42&scope=bot&permissions=" + invitePermissions
	if got != want {
		t.Errorf("inviteURL = %q, want %q", got, want)
	}

	got = inviteURL(resolveInviteAppID(context.Background(), &fakeAPI{appIDErr: errors.New("nope")}, "bot-9"))
	if !strings.Contains(got, "client_id=bot-9") {
		t.Errorf("app-info failure should fall back to the bot id: %q", got)
	}
	if !strings.Contains(got, "permissions=309774617664") {
		t.Errorf("permissions not baked in: %q", got)
	}
}
