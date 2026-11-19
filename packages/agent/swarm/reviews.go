package swarm

// The mark of a child that unattended work started, and the socket on which
// that child asks its supervisor to count a review request.
//
// 🔑 A native child is a process of its own, with a gate of its own. The
// host's rules for work that no person began are rungs of the host's gate,
// so they do not reach the child. The mark tells the child to put the same
// rungs on its own gate. The count of review requests is in the host, under
// one lock, so the child asks for it here.
//
// The swarm carries the mark and reads none of it. The host writes it at the
// spawn and reads it back when the child asks.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider/lineframe"
)

// Unattended names the chain that started an agent, when no person began
// that chain. A nil mark is an agent of attended work.
type Unattended struct {
	// Team is the host's id for the team that the chain is in.
	Team string `json:"team"`
	// Member is the team member whose turn started the agent. A review
	// request of the agent counts as a request of this member.
	Member string `json:"member"`
	// Chain is the root of the chain.
	Chain string `json:"chain"`
}

// ErrUnattendedBackend refuses a worker backend for an agent of unattended
// work. A backend names a process with no terva gate, so the rules that the
// mark stands for could not reach it.
var ErrUnattendedBackend = errors.New("swarm: work that no person began cannot start an agent on a worker backend")

// ProcessMark returns the mark that this process itself works under, or nil.
// A native child of unattended work has the supervisor's variable in its
// environment.
//
// 🚨 Such a process is unattended in all that it does, so each agent that it
// starts is unattended too (SpawnReq). The mark names no chain, because the
// process does not know one. The agent's review requests then reach a
// supervisor with no count, and each one is refused.
func ProcessMark() *Unattended {
	if envcompat.Get(UnattendedEnv) == "" {
		return nil
	}
	return &Unattended{}
}

// cloneMark copies a mark, so a caller that keeps its own cannot change the
// agent's.
func cloneMark(m *Unattended) *Unattended {
	if m == nil {
		return nil
	}
	c := *m
	return &c
}

// UnattendedEnv is the variable that tells a native child of its mark. The
// runner sets it with the product's prefix, and the child reads it through
// envcompat.
//
// 🚨 The child holds no id of the chain. Its socket names it, and the host
// reads the chain from the agent's record, so a child cannot count against a
// chain that is not its own.
const UnattendedEnv = "SWARM_UNATTENDED"

// ReviewSocketPath is the path of an agent's review socket. An inbox path
// ends in .sock (inboxSocketPath), and this replaces that end. So the path
// is shorter than one that already fits a unix socket, and it is the
// agent's own for the same reason the inbox is.
func ReviewSocketPath(inbox string) string {
	return strings.TrimSuffix(inbox, ".sock") + ".rv"
}

// The waits of one exchange. The count takes the router's lock and writes one
// line, so an answer comes in milliseconds. A wait that ends refuses.
const (
	reviewDialWait   = 5 * time.Second
	reviewAnswerWait = 30 * time.Second
	reviewFrameMax   = 4096
)

// reviewAsk is the one question of a connection.
type reviewAsk struct {
	Reviews int `json:"reviews"`
}

// reviewReply is its answer.
//
// 🚨 OK must be true for a request that may run. An answer with no key, such
// as one that another program wrote, allows nothing.
type reviewReply struct {
	OK   bool   `json:"ok"`
	Deny string `json:"deny,omitempty"`
}

// chmodSocket sets the mode of a socket's file. A test replaces it with a
// call that fails.
var chmodSocket = os.Chmod

// serveReviews opens the review socket at path, and answers each question
// with count until Close. count returns why the requests are refused, or an
// empty text when it counted them.
func serveReviews(path string, count func(n int) string) (io.Closer, error) {
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// A socket that an earlier run of this agent left.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// 🔑 Close removes the file only while it is still this listener's
	// (reviewServer.Close), so Go's own removal by name is off.
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	// 🚨 The mode is the guard, and net.Listen follows the umask. A socket
	// that kept a wider mode must not serve. The directory is private
	// (privfs), so the time between the two calls is no way in. Windows has
	// no mode to set.
	if err := chmodSocket(path, 0o600); err != nil && runtime.GOOS != "windows" {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set the mode of the review socket: %w", err)
	}
	// A failure leaves made nil, and Close then removes nothing.
	made, _ := os.Stat(path)
	srv := &reviewServer{ln: ln, path: path, made: made}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go answerReview(conn, count)
		}
	}()
	return srv, nil
}

type reviewServer struct {
	ln   net.Listener
	path string
	// made is the socket's file as this server made it.
	made os.FileInfo
}

// Close stops the server, and removes the socket's file when it is still
// the one that this server made.
//
// 🚨 The path is the agent's, and a later runner of the same agent listens
// at it again. A runner that winds down after that must not remove the new
// socket: each review request of the new child would then get no answer.
func (s *reviewServer) Close() error {
	err := s.ln.Close()
	if now, statErr := os.Stat(s.path); statErr == nil && s.made != nil && os.SameFile(now, s.made) {
		_ = os.Remove(s.path)
	}
	return err
}

// answerReview answers the one question of conn. A question that it cannot
// read gets a refusal, so the child has an answer and no wait.
func answerReview(conn net.Conn, count func(n int) string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(reviewAnswerWait))
	reply := func(r reviewReply) {
		b, _ := json.Marshal(r)
		_, _ = conn.Write(append(b, '\n'))
	}
	line, tooLong, err := lineframe.ReadFrame(bufio.NewReader(conn), reviewFrameMax)
	if tooLong {
		reply(reviewReply{Deny: "the question was too long"})
		return
	}
	if len(bytes.TrimSpace(line)) == 0 && err != nil {
		return
	}
	var ask reviewAsk
	if json.Unmarshal(bytes.TrimSpace(line), &ask) != nil || ask.Reviews < 1 {
		reply(reviewReply{Deny: "the question named no count of review requests"})
		return
	}
	if why := count(ask.Reviews); why != "" {
		reply(reviewReply{Deny: why})
		return
	}
	reply(reviewReply{OK: true})
}

// AskReviews asks the supervisor of the child with this inbox to count n
// review requests. deny is the supervisor's refusal, in the words the model
// reads. err says that no answer came.
//
// 🚨 The caller refuses the tool call on either one. Only an answer that
// says OK lets the request run.
func AskReviews(ctx context.Context, inbox string, n int) (deny string, err error) {
	ctx, cancel := context.WithTimeout(ctx, reviewAnswerWait)
	defer cancel()
	d := net.Dialer{Timeout: reviewDialWait}
	conn, err := d.DialContext(ctx, "unix", ReviewSocketPath(inbox))
	if err != nil {
		return "", fmt.Errorf("the supervisor of this sub-agent does not answer: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// A turn that ends closes the connection, so the read returns.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	b, _ := json.Marshal(reviewAsk{Reviews: n})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return "", fmt.Errorf("the question did not reach the supervisor: %w", err)
	}
	line, tooLong, err := lineframe.ReadFrame(bufio.NewReader(conn), reviewFrameMax)
	if tooLong {
		return "", errors.New("the supervisor's answer was too long")
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", fmt.Errorf("the supervisor gave no answer: %w", err)
	}
	var reply reviewReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return "", fmt.Errorf("the supervisor's answer does not parse: %w", err)
	}
	switch {
	case reply.Deny != "":
		return reply.Deny, nil
	case !reply.OK:
		return "", errors.New("the supervisor's answer allowed nothing")
	}
	return "", nil
}
