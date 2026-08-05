package provider_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/provider"
)

// liveProbeBaseURL is the endpoint under test. It is a literal rather than
// provider.codexDefaultBaseURL because this probe must live in the external
// test package (resolving a real credential means importing agent/build, which
// imports provider). TestCodexDefaultBaseURLMatchesTheLiveProbeTarget pins the
// two together so the constant cannot drift away from this probe silently.
const liveProbeBaseURL = "https://chatgpt.com/backend-api/codex/responses"

// livePayloadFloor is the prompt size below which OpenAI documents that prompt
// caching does not apply at all. A cached_tokens of 0 under this floor is the
// spec, not a finding — so the probe refuses to interpret such a run.
const livePayloadFloor = 1024

// fnv32 gives each body a short stable tag, so the filler can be unique per
// body without carrying the whole salt on every one of its iterations.
func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return h
}

// The client-identity cross-read probe: does the ChatGPT Codex backend's
// prompt cache partition on WHO is asking?
//
// Why this is the question left. The cache is content-addressed
// (prompt_cache_key is a routing hint, not a partition — measured, 3/3 reps
// identical to the token), and --dump-prompt=wire proved terva's request BODY
// is append-only across a collapse: a 94-item prefix held byte-identical over
// 21 dispatch boundaries while the backend returned nothing but the floor.
// Identical content, content-addressed cache, still a miss. So whatever
// discriminates is not in the body. What is left outside it is the bearer
// token, chatgpt-account-id, and the client-identity headers.
//
// terva sends `originator: terva` with its own user-agent and NO session-id
// header. The upstream project terva forked from (commit b58450d9) reports
// that this backend
// load-sheds client identities it does not recognize — unknown originator /
// user-agent pairs draw "Our servers are currently overloaded" near-
// deterministically while Codex CLI requests succeed. That is a claim about
// ADMISSION, not about caching. This probe asks the caching question directly.
//
// The design is a CROSS-READ, not two independent arms. Per rep:
//
//	control: body_c under identity L, twice     -> does L read its own write?
//	cross:   body_x under identity L, then F    -> does F read L's write?
//
// The control is what makes the cross column mean anything: a cross miss is
// evidence only if the same body under the same identity demonstrably hits.
// A rep whose control does not hit is VOID and reported as such, never folded
// into the verdict.
//
// Confounds this harness holds, each of which has already spoiled an
// experiment on this backend:
//
//   - Bodies are UNIQUE per rep and per pair. The cache is content-addressed,
//     so a shared body lets a later rep read what an earlier one paid to
//     write, and every arm after the first reads high.
//   - The LEADER alternates. Every earlier A/B on this question let arm A go
//     first, and one of them read backwards at n=1.
//   - The body is REAL-SIZED. A toy request carries no input_tokens_details at
//     all, which makes "cached_tokens: 0" and "the field was never there"
//     indistinguishable — that nearly mis-reported the /compact result. Here
//     cached_tokens decodes into a *int so absent and present-and-zero stay
//     different facts, and an absent control VOIDS its rep.
//
// Skipped unless TERVA_LIVE_IDENTITY_AB is set, because it SPENDS REAL MONEY:
// 4 calls per rep at roughly 20k prompt tokens each.
//
//	TERVA_LIVE_IDENTITY_AB=1 [TERVA_LIVE_IDENTITY_REPS=3] \
//	  go test ./packages/provider -run TestLiveCodexIdentityCrossRead -v -timeout 30m
func TestLiveCodexIdentityCrossRead(t *testing.T) {
	if os.Getenv("TERVA_LIVE_IDENTITY_AB") == "" {
		t.Skip("live probe: set TERVA_LIVE_IDENTITY_AB=1 (spends real money)")
	}

	// Through terva's own resolver: auth.json is encrypted at rest, and this
	// path also refreshes an expired OAuth token. The token is never printed.
	token, method, accountID, err := build.ResolveCredentialFull("openai-codex", "")
	if err != nil {
		t.Fatalf("resolve openai-codex credential: %v", err)
	}
	if method != "oauth" {
		t.Fatalf("openai-codex credential is %q, not oauth — this probe measures the "+
			"ChatGPT subscription backend and an API key does not reach it", method)
	}
	t.Logf("credential: method=%s account=%s token=<%d chars, not logged>", method, accountID, len(token))

	reps := 3
	if v := os.Getenv("TERVA_LIVE_IDENTITY_REPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("TERVA_LIVE_IDENTITY_REPS=%q is not a positive integer", v)
		}
		reps = n
	}
	model := "gpt-5.6-sol"
	if v := os.Getenv("TERVA_LIVE_IDENTITY_MODEL"); v != "" {
		model = v
	}

	// A per-RUN nonce. Without it the salts are deterministic and every run
	// re-sends the previous run's exact bytes, so rep 1's "cold" call reads
	// what the last run paid to write — observed, and it produced a 42k-token
	// cache hit on a request that had never been sent in that process.
	//
	// The content-addressing rule that makes bodies unique WITHIN a run
	// applies just as hard ACROSS runs; the cache does not know where a
	// process boundary is. Override to replay a specific run's bodies.
	nonce := os.Getenv("TERVA_LIVE_IDENTITY_NONCE")
	if nonce == "" {
		nonce = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	t.Logf("run nonce: %s (set TERVA_LIVE_IDENTITY_NONCE to replay these exact bodies)", nonce)

	terva := identity{
		name:       "terva",
		originator: "terva",
		userAgent:  fmt.Sprintf("terva (%s %s)", "darwin", "arm64"),
		// terva sends no session-id header at all. That absence is part of the
		// identity under test, so it is not quietly filled in here.
	}
	codexCLI := identity{
		name:       "codex_cli_rs",
		originator: "codex_cli_rs",
		userAgent:  "codex_cli_rs/0.0.0",
		sessionID:  true,
	}

	type repResult struct {
		rep              int
		leader, follower string
		selfTerva        callResult // terva writes, terva re-reads
		selfCodex        callResult // codex_cli_rs writes, codex_cli_rs re-reads
		crossRead        callResult // leader writes, FOLLOWER re-reads
	}
	var results []repResult

	// The pilot taught this shape. Running the control only under the LEADER
	// cannot tell "the harness does not cache" from "THIS identity does not
	// cache" — and the latter is the hypothesis. So every rep now takes a
	// self-read control under BOTH identities, and those two controls are
	// themselves a result, not just a gate on the cross column.
	for rep := 0; rep < reps; rep++ {
		// Alternate the leader: every earlier A/B on this backend let one arm
		// go first for every rep, and that is how one of them read backwards.
		leader, follower := terva, codexCLI
		if rep%2 == 1 {
			leader, follower = codexCLI, terva
		}
		res := repResult{rep: rep + 1, leader: leader.name, follower: follower.name}

		// Three distinct bodies per rep, distinct across reps: a shared body
		// would let this rep read what an earlier one paid to write.
		bodyT := livePayload(model, fmt.Sprintf("%s-rep%d-self-terva", nonce, rep))
		bodyC := livePayload(model, fmt.Sprintf("%s-rep%d-self-codex", nonce, rep))
		bodyX := livePayload(model, fmt.Sprintf("%s-rep%d-cross", nonce, rep))

		selfRead := func(label string, body []byte, id identity) callResult {
			cold, err := liveCodexCall(t, token, accountID, body, id)
			if err != nil {
				t.Fatalf("rep %d %s cold (%s): %v", rep+1, label, id.name, err)
			}
			// OpenAI documents prompt caching as applying only at or above
			// 1024 prompt tokens. Below that a cached_tokens of 0 is the
			// DOCUMENTED behavior and says nothing about identity — so a
			// too-small payload must stop the run, not quietly produce zeros
			// that read like a finding.
			if cold.input < livePayloadFloor {
				t.Fatalf("rep %d %s cold (%s): request billed %d input tokens, below the %d-token "+
					"caching floor — a cached_tokens of 0 here is documented behavior, not evidence. "+
					"Grow livePayload before reading anything into this run.",
					rep+1, label, id.name, cold.input, livePayloadFloor)
			}
			// A body this process has never sent cannot legitimately read from
			// cache. If it does, these exact bytes were written by an EARLIER
			// run and every number in this rep describes that run instead of
			// this one. Observed once, at 42k tokens, and it read like a
			// finding — so it fails loudly now rather than being interpreted.
			if cold.hit() {
				t.Fatalf("rep %d %s cold (%s): a first-send read %d tokens from cache. "+
					"These bytes are not fresh — the run nonce is not doing its job. "+
					"Nothing in this run is interpretable; fix the salt and re-run.",
					rep+1, label, id.name, *cold.cached)
			}
			warm, err := liveCodexCall(t, token, accountID, body, id)
			if err != nil {
				t.Fatalf("rep %d %s warm (%s): %v", rep+1, label, id.name, err)
			}
			t.Logf("  rep %d %-10s %-12s cold %s -> warm %s", rep+1, label, id.name, cold, warm)
			return warm
		}
		// Alternate which identity takes its self-read FIRST, on the same
		// parity as the cross leader. Without this, terva always leads and
		// "the first pair of a run does not cache" survives as a rival
		// explanation for the exact column the verdict is read from — which
		// is how earlier A/Bs on this backend went wrong.
		if leader.name == terva.name {
			res.selfTerva = selfRead("self", bodyT, terva)
			res.selfCodex = selfRead("self", bodyC, codexCLI)
		} else {
			res.selfCodex = selfRead("self", bodyC, codexCLI)
			res.selfTerva = selfRead("self", bodyT, terva)
		}

		xCold, err := liveCodexCall(t, token, accountID, bodyX, leader)
		if err != nil {
			t.Fatalf("rep %d cross cold (%s): %v", rep+1, leader.name, err)
		}
		if xCold.hit() {
			t.Fatalf("rep %d cross cold (%s): a first-send read %d tokens from cache — "+
				"stale bytes, see the self-read tripwire.", rep+1, leader.name, *xCold.cached)
		}
		res.crossRead, err = liveCodexCall(t, token, accountID, bodyX, follower)
		if err != nil {
			t.Fatalf("rep %d cross read (%s): %v", rep+1, follower.name, err)
		}
		t.Logf("  rep %d %-10s %s -> %s: cold %s -> read %s",
			rep+1, "cross", leader.name, follower.name, xCold, res.crossRead)
		results = append(results, res)
	}

	// Split every self-read by POSITION as well as identity. The n=4 run showed
	// terva hitting only when it led, which ordering alone would also explain —
	// so the two have to be reported apart or the table cannot distinguish
	// "terva does not cache" from "whoever goes second does not cache".
	var tervaSelf, codexSelf, crossHit int
	var tervaLed, tervaLedHit, tervaFollowed, tervaFollowedHit int
	var codexLed, codexLedHit, codexFollowed, codexFollowedHit int
	for _, r := range results {
		tervaFirst := r.leader == "terva"
		if r.selfTerva.hit() {
			tervaSelf++
		}
		if r.selfCodex.hit() {
			codexSelf++
		}
		if r.crossRead.hit() {
			crossHit++
		}
		if tervaFirst {
			tervaLed++
			codexFollowed++
			if r.selfTerva.hit() {
				tervaLedHit++
			}
			if r.selfCodex.hit() {
				codexFollowedHit++
			}
		} else {
			tervaFollowed++
			codexLed++
			if r.selfTerva.hit() {
				tervaFollowedHit++
			}
			if r.selfCodex.hit() {
				codexLedHit++
			}
		}
	}
	n := len(results)
	t.Logf("")
	t.Logf("═══ verdict (n=%d) ═══", n)
	t.Logf("terva        self-read hits: %d/%d   (led %d/%d, followed %d/%d)",
		tervaSelf, n, tervaLedHit, tervaLed, tervaFollowedHit, tervaFollowed)
	t.Logf("codex_cli_rs self-read hits: %d/%d   (led %d/%d, followed %d/%d)",
		codexSelf, n, codexLedHit, codexLed, codexFollowedHit, codexFollowed)
	t.Logf("cross-identity read hits:    %d/%d", crossHit, n)
	if codexSelf > tervaSelf && codexFollowedHit == codexFollowed && codexFollowed > 0 {
		t.Logf("   ↑ codex_cli_rs hits even when it goes SECOND, so 'whoever follows misses'")
		t.Logf("     does not explain terva's misses. Position is controlled for.")
	}
	if n < 2 {
		t.Logf("⚠️  n=1 CANNOT settle this: with one rep only one identity took its self-read")
		t.Logf("   first, so ordering is not yet controlled. Run an EVEN number of reps (>=2)")
		t.Logf("   so each identity leads equally often before treating any row as a result.")
	} else if n%2 == 1 {
		t.Logf("⚠️  n is ODD, so one identity led one more time than the other. Prefer an even n.")
	}

	switch {
	case tervaSelf == 0 && codexSelf == n:
		t.Logf("⇒ 🚨 THE IDENTITY IS THE VARIABLE. Same bytes, same account, same endpoint: the")
		t.Logf("   Codex CLI identity reads its own cache write and terva's NEVER does. That is")
		t.Logf("   the mechanism the collapse investigation was missing. Confirm at higher n")
		t.Logf("   before acting, and note this is an ADMISSION/ROUTING claim, not a proof that")
		t.Logf("   it explains every collapse.")
	case tervaSelf == 0 && codexSelf == 0:
		t.Logf("⇒ NEITHER identity reads its own write. This measured NOTHING about identity —")
		t.Logf("   the harness is not reproducing caching at all. Check payload size and shape")
		t.Logf("   against a known-good terva dispatch (--dump-prompt=wire) before spending more.")
	case tervaSelf == n && codexSelf == n && crossHit == n:
		t.Logf("⇒ identity does NOT partition the cache: both self-read AND cross-read hit.")
		t.Logf("   The lever is DEAD; the collapse is elsewhere.")
	case tervaSelf == n && codexSelf == n && crossHit == 0:
		t.Logf("⇒ identity PARTITIONS the cache: each identity reads its own write, neither")
		t.Logf("   reads the other's. Confirm at higher n before acting.")
	case codexSelf == n && tervaSelf < n && crossHit == 0:
		t.Logf("⇒ 🚨 ASYMMETRIC, and the asymmetry is the finding. codex_cli_rs reads its own")
		t.Logf("   write EVERY time (%d/%d); terva manages it %d/%d; neither reads the other's.", codexSelf, n, tervaSelf, n)
		t.Logf("   Note this is NOT a clean partition: a partitioned cache would still let terva")
		t.Logf("   read reliably inside its own. Flaky-for-one-identity, perfect-for-the-other")
		t.Logf("   looks like DEGRADED SERVICE OR ROUTING, which is the shape the collapse has.")
	default:
		t.Logf("⇒ MIXED. Not a result yet: add reps.")
	}
}

// identity is the client-identity header set under test — the ONLY thing that
// differs between the two arms. Same token, same account, same bytes.
type identity struct {
	name       string
	originator string
	userAgent  string
	// sessionID sends a session-id header, stable per body so the two calls of
	// a pair present the same session (what the Codex CLI does). terva sends
	// none, so its ABSENCE is part of that identity rather than a free knob.
	sessionID bool
}

// livePayload builds a real-sized request body. Size is a correctness
// requirement, not just a cost one: below roughly a thousand tokens the
// backend returns no input_tokens_details at all and every cached_tokens
// reading becomes unfalsifiable.
//
// salt makes each body unique. Without it the content-addressed cache would
// serve a later rep from an earlier rep's write and every arm would read high.
// The filler is non-repeating for the same reason: a long run of identical
// text is a prefix another probe may already have paid to write.
func livePayload(model, salt string) []byte {
	var sb strings.Builder
	// The salt goes at the HEAD, which is what makes bodies independent:
	// caching matches on PREFIX, so two bodies that diverge in their first
	// characters share no cacheable prefix no matter what follows.
	sb.WriteString("Prompt-cache probe. Salt: " + salt + ". Inert filler follows. ")
	// A short per-body tag inside the loop rather than the whole salt: the
	// full salt repeated thousands of times inflated a call from 43k to 79k
	// tokens once the run nonce was added, which is pure cost. Deliberately
	// well above the 1024-token floor and well below anything that makes a
	// variant sweep expensive; livePayloadFloor makes the backend confirm the
	// size rather than trusting this arithmetic.
	tag := fnv32(salt)
	for i := 0; i < 1600; i++ {
		fmt.Fprintf(&sb, "%08x-%d ", tag^uint32(i)*2654435761, i)
	}
	req := map[string]any{
		"model":  model,
		"store":  false,
		"stream": true,
		"instructions": "You are a fixture in a cache measurement. " +
			"Answer with the single word: ok.",
		"input": []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": sb.String() + "\n\nReply with exactly: ok",
			}},
		}},
		"parallel_tool_calls": true,
		// Held CONSTANT across both identities so the key cannot be the
		// variable. It is not a partition anyway (measured), but leaving it
		// free would reintroduce a settled question as a confound.
		"prompt_cache_key": "identity-probe-" + salt,
	}
	body, err := json.Marshal(req)
	if err != nil {
		panic(err) // a map we built ourselves; unmarshalable is a bug, not a condition
	}
	return body
}

// callResult is one dispatch's usage. cached is a *int because "the field was
// absent" and "the field said zero" are different facts — conflating them is
// how the /compact result nearly went wrong.
//
// input is carried alongside it because cached_tokens alone cannot tell a
// cache that did not fire from a payload that never landed. The pilot run
// showed exactly why: a bare "cached 0" is unreadable without the size of the
// request it is zero out of.
type callResult struct {
	cached *int
	input  int
	output int
}

func (r callResult) String() string {
	c := "absent"
	if r.cached != nil {
		c = strconv.Itoa(*r.cached)
	}
	return fmt.Sprintf("cached=%s/in=%d", c, r.input)
}

func (r callResult) hit() bool { return r.cached != nil && *r.cached > 0 }

// liveCodexCall sends body under id and returns that dispatch's usage.
func liveCodexCall(t *testing.T, token, accountID string, body []byte, id identity) (callResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", liveProbeBaseURL, bytes.NewReader(body))
	if err != nil {
		return callResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("openai-beta", "responses=experimental")
	req.Header.Set("originator", id.originator)
	req.Header.Set("user-agent", id.userAgent)
	if id.sessionID {
		req.Header.Set("session-id", fmt.Sprintf("%08x-0000-4000-8000-%012x", len(body), len(body)*7919))
	}

	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return callResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return callResult{}, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var p struct {
			Type     string `json:"type"`
			Response struct {
				Usage *struct {
					InputTokens        int `json:"input_tokens"`
					OutputTokens       int `json:"output_tokens"`
					InputTokensDetails *struct {
						CachedTokens *int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &p) != nil {
			continue
		}
		if p.Type != "response.completed" && p.Type != "response.done" {
			continue
		}
		if p.Response.Usage == nil {
			return callResult{}, fmt.Errorf("response.completed carried no usage")
		}
		out := callResult{
			input:  p.Response.Usage.InputTokens,
			output: p.Response.Usage.OutputTokens,
		}
		if d := p.Response.Usage.InputTokensDetails; d != nil {
			out.cached = d.CachedTokens
		}
		return out, nil
	}
	if err := sc.Err(); err != nil {
		return callResult{}, err
	}
	return callResult{}, fmt.Errorf("stream ended with no response.completed")
}

// TestLiveCodexIdentityHeaderDecomposition asks which of the three headers
// carries the effect that TestLiveCodexIdentityCrossRead measured.
//
// That test established the WHAT at n=6: on one account, one token, one
// endpoint, with the same bytes, the Codex CLI identity reads its own cache
// write 6/6 and terva's does 1/6. It varied all three headers at once, so it
// cannot say which one matters. This one holds two fixed and moves the third.
//
// The variant that decides what terva should DO is `terva+session-id`. terva
// sends no session-id header at all — not a different value, none. If adding
// only that restores caching, the fix is sending a field we simply omit, with
// no impersonation and no ToS question. If instead `originator` alone carries
// it, then any fix means presenting as the Codex CLI, which is a judgment call
// rather than a bug fix.
//
// Measurement is the SELF-READ only (does this identity read its own write?),
// because that is the actionable question; cross-identity reads are already
// covered. Variant order rotates by rep so no variant sits permanently in the
// first slot — position was a live rival explanation in the n=6 run until it
// was controlled, and one rotation is cheaper than re-litigating it.
//
//	TERVA_LIVE_IDENTITY_AB=1 [TERVA_LIVE_IDENTITY_REPS=4] \
//	  go test ./packages/provider -run TestLiveCodexIdentityHeaderDecomposition -v -timeout 30m
func TestLiveCodexIdentityHeaderDecomposition(t *testing.T) {
	if os.Getenv("TERVA_LIVE_IDENTITY_AB") == "" {
		t.Skip("live probe: set TERVA_LIVE_IDENTITY_AB=1 (spends real money)")
	}
	token, method, accountID, err := build.ResolveCredentialFull("openai-codex", "")
	if err != nil {
		t.Fatalf("resolve openai-codex credential: %v", err)
	}
	if method != "oauth" {
		t.Fatalf("openai-codex credential is %q, not oauth", method)
	}

	reps := 4
	if v := os.Getenv("TERVA_LIVE_IDENTITY_REPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("TERVA_LIVE_IDENTITY_REPS=%q is not a positive integer", v)
		}
		reps = n
	}
	model := "gpt-5.6-sol"
	if v := os.Getenv("TERVA_LIVE_IDENTITY_MODEL"); v != "" {
		model = v
	}
	nonce := os.Getenv("TERVA_LIVE_IDENTITY_NONCE")
	if nonce == "" {
		nonce = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	t.Logf("run nonce: %s", nonce)

	const tervaUA = "terva (darwin arm64)"
	const cliUA = "codex_cli_rs/0.0.0"

	// Two baselines that reproduce the known result, and three single-header
	// moves off terva's shape. Each move changes exactly ONE thing from
	// `terva`, so a hit names its own cause.
	variants := []identity{
		{name: "terva(baseline)", originator: "terva", userAgent: tervaUA},
		{name: "codex_cli(full)", originator: "codex_cli_rs", userAgent: cliUA, sessionID: true},
		{name: "terva+session", originator: "terva", userAgent: tervaUA, sessionID: true},
	}
	// originator-only and useragent-only are DELIBERATELY GONE. At n=4 they
	// scored 1/4 each against a 0/4 baseline and a 4/4 full shape — neither
	// carries the effect, and keeping them would spend a fifth of every
	// subsequent run re-confirming a negative. The question that is actually
	// open is narrower: session-id scored 3/4, so is it FULLY sufficient or
	// only NECESSARY? Three variants at higher n answers that; five at low n
	// answers nothing. Set TERVA_LIVE_IDENTITY_ALLVARIANTS=1 to restore them.
	if os.Getenv("TERVA_LIVE_IDENTITY_ALLVARIANTS") != "" {
		variants = append(variants,
			identity{name: "originator-only", originator: "codex_cli_rs", userAgent: tervaUA},
			identity{name: "useragent-only", originator: "terva", userAgent: cliUA},
		)
	}

	hits := make([]int, len(variants))
	for rep := 0; rep < reps; rep++ {
		// Rotate the running order so no variant is always first.
		for k := range variants {
			idx := (k + rep) % len(variants)
			v := variants[idx]
			body := livePayload(model, fmt.Sprintf("%s-rep%d-%s", nonce, rep, v.name))

			cold, err := liveCodexCall(t, token, accountID, body, v)
			if err != nil {
				t.Fatalf("rep %d %s cold: %v", rep+1, v.name, err)
			}
			if cold.input < livePayloadFloor {
				t.Fatalf("rep %d %s: %d input tokens is below the %d-token caching floor",
					rep+1, v.name, cold.input, livePayloadFloor)
			}
			if cold.hit() {
				t.Fatalf("rep %d %s cold: a first-send read %d tokens from cache — stale bytes",
					rep+1, v.name, *cold.cached)
			}
			warm, err := liveCodexCall(t, token, accountID, body, v)
			if err != nil {
				t.Fatalf("rep %d %s warm: %v", rep+1, v.name, err)
			}
			if warm.hit() {
				hits[idx]++
			}
			t.Logf("  rep %d slot%d %-16s cold %s -> warm %s", rep+1, k, v.name, cold, warm)
		}
	}

	t.Logf("")
	t.Logf("═══ which header carries it? (n=%d) ═══", reps)
	for i, v := range variants {
		t.Logf("  %-16s self-read %d/%d", v.name, hits[i], reps)
	}

	base, full, session := hits[0], hits[1], hits[2]

	// Pool by the thing actually under test. Reading variant-by-variant at
	// small n invites over-reading a clean-looking table: across runs this
	// effect is PROBABILISTIC, not a switch, and per-variant rows of 4/4 or
	// 6/6 have already been followed by a 5/8 for the same variant. The
	// session-id present/absent split is the hypothesis and pools the reps.
	var withHits, withTrials, withoutHits, withoutTrials int
	for i, v := range variants {
		if v.sessionID {
			withHits += hits[i]
			withTrials += reps
		} else {
			withoutHits += hits[i]
			withoutTrials += reps
		}
	}
	t.Logf("")
	t.Logf("pooled by session-id:  present %d/%d   absent %d/%d",
		withHits, withTrials, withoutHits, withoutTrials)
	t.Logf("(pool across runs before concluding — single runs of this have swung 4/4 to 5/8)")
	t.Logf("")
	// Without this separation the session row is uninterpretable, so it stops
	// the run rather than letting a tidy-looking table describe a regime where
	// caching was not working in the first place.
	if full <= base {
		t.Fatalf("the baselines did not reproduce: codex_cli(full)=%d is not above terva(baseline)=%d. "+
			"Without that separation none of the variant rows mean anything.", full, base)
	}
	switch {
	case session >= full && session > base:
		t.Logf("⇒ ✅ session-id alone does AT LEAST as well as the full Codex CLI shape")
		t.Logf("   (%d/%d vs %d/%d, base %d/%d) while keeping originator=terva and terva's", session, reps, full, reps, base, reps)
		t.Logf("   own user-agent. THE FIX IS NOT IMPERSONATION — terva sends no session-id.")
		t.Logf("   🪤 Wire it with a value STABLE across a conversation's dispatches; a fresh")
		t.Logf("      id per request reproduces the baseline while looking applied.")
		t.Logf("   ⚠️  session>=full is NOT evidence session-id beats the full shape; at this n")
		t.Logf("      the two are not separable. It only means nothing is lost by not spoofing.")
	case session > base && session < full:
		t.Logf("⇒ session-id is NECESSARY but maybe not sufficient: %d/%d vs full %d/%d vs base %d/%d.",
			session, reps, full, reps, base, reps)
		t.Logf("   Add reps — at small n this gap is not separable from noise.")
	default:
		t.Logf("⇒ session-id did NOT separate from baseline this run (%d/%d vs %d/%d).", session, reps, base, reps)
		t.Logf("   One run is not a refutation either — POOL with previous runs before deciding,")
		t.Logf("   and re-open the other headers with TERVA_LIVE_IDENTITY_ALLVARIANTS=1.")
	}
}

// TestLiveCodexShippedClientReadsItsOwnCache is the end-to-end check on the
// SHIPPED path, and it exists because the A/B above does not cover it.
//
// That harness hand-built raw JSON and set headers itself. It proved the
// backend's behaviour, not terva's: the real client sends a different body
// (instructions, tools, reasoning config, Include, store:false) and derives
// its session-id through codexSessionID rather than being handed one. A fix
// that works in a synthetic request and not through provider.Client would be
// invisible to every test written so far — the one-production-caller trap.
//
// So this drives provider.NewOpenAICodex twice with the SAME PromptCacheKey,
// exactly as core.Agent does across a conversation's dispatches, and asserts
// the second dispatch reads the first one's write.
//
//	TERVA_LIVE_IDENTITY_AB=1 \
//	  go test ./packages/provider -run TestLiveCodexShippedClientReadsItsOwnCache -v -timeout 20m
func TestLiveCodexShippedClientReadsItsOwnCache(t *testing.T) {
	if os.Getenv("TERVA_LIVE_IDENTITY_AB") == "" {
		t.Skip("live probe: set TERVA_LIVE_IDENTITY_AB=1 (spends real money)")
	}
	token, method, accountID, err := build.ResolveCredentialFull("openai-codex", "")
	if err != nil {
		t.Fatalf("resolve openai-codex credential: %v", err)
	}
	if method != "oauth" {
		t.Fatalf("openai-codex credential is %q, not oauth", method)
	}
	model := "gpt-5.6-sol"
	if v := os.Getenv("TERVA_LIVE_IDENTITY_MODEL"); v != "" {
		model = v
	}
	nonce := os.Getenv("TERVA_LIVE_IDENTITY_NONCE")
	if nonce == "" {
		nonce = strconv.FormatInt(time.Now().UnixNano(), 36)
	}

	// The cache key core.Agent would carry: stable across both dispatches,
	// unique to this run so neither call reads a previous run's write.
	cacheKey := "live-probe-" + nonce
	var filler strings.Builder
	tag := fnv32(cacheKey)
	for i := 0; i < 1600; i++ {
		fmt.Fprintf(&filler, "%08x-%d ", tag^uint32(i)*2654435761, i)
	}
	req := provider.Request{
		Model:          model,
		System:         "You are a fixture in a cache measurement. Answer with the single word: ok.",
		PromptCacheKey: cacheKey,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: filler.String() + "\n\nReply with exactly: ok"}},
		}},
	}

	dispatch := func(label string) provider.Usage {
		c := provider.NewOpenAICodex(token, accountID, "")
		ch, err := c.Stream(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: Stream: %v", label, err)
		}
		var usage provider.Usage
		for ev := range ch {
			switch e := ev.(type) {
			case provider.EventUsage:
				usage = e.Usage
			case provider.EventDone:
				if e.Err != nil {
					t.Fatalf("%s: stream error: %v", label, e.Err)
				}
			}
		}
		t.Logf("  %-5s input=%d cache_read=%d cache_write=%d",
			label, usage.InputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
		return usage
	}

	// A fresh client per dispatch on purpose: a token refresh rebuilds the
	// client mid-conversation, so the session id must survive that. It does
	// only because it is derived from the request's cache key rather than
	// held in client state — this is what pins that.
	cold := dispatch("cold")
	if cold.CacheReadTokens > 0 {
		t.Fatalf("cold dispatch read %d tokens from cache — these bytes are not fresh",
			cold.CacheReadTokens)
	}
	warm := dispatch("warm")

	if warm.CacheReadTokens == 0 {
		t.Errorf("the shipped client did not read its own cache write.\n"+
			"Same PromptCacheKey (%q), same bytes, two dispatches — the second read 0.\n"+
			"Either the session-id header is not reaching the wire through this path, or "+
			"the backend declined to serve it this time (the effect is ~83%%, not certain: "+
			"re-run before concluding the fix is broken).", cacheKey)
		return
	}
	pct := float64(warm.CacheReadTokens) / float64(warm.CacheReadTokens+warm.InputTokens) * 100
	t.Logf("✅ the shipped client read %d tokens (%.1f%% of prompt) from its own write",
		warm.CacheReadTokens, pct)
}
