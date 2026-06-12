// secret — demonstrates collecting a secret from the user via a
// masked panel. The model never sees the value; it only sees the
// outcome (success or failure).
//
// Registers one LLM-callable tool:
//
//	fetch_with_password(url: string)
//
// When the model calls it, a panel opens with a masked password
// field. The user types the password and presses Enter; the tool
// goroutine uses it directly to perform a (fake) authenticated fetch
// and returns only "fetched successfully" or an error to the model.
// The password exists only in the extension process's memory and is
// never written to any JSON frame or the transcript.
//
// Build:
//
//	cd examples/extensions/secret
//	go build -o secret .
//
// Install:
//
//	terva ext install .
//
// Try it — ask terva something like:
//
//	"Fetch https://internal.example.com/report — it needs a password."
//
// The model calls fetch_with_password; a masked password panel opens.
// Type anything and press Enter. The model receives the result without
// ever seeing what you typed.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/ext"
)

const schema = `{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The URL to fetch."
    }
  },
  "required": ["url"]
}`

func main() {
	e := ext.New("secret", "1.0.0")

	var mu sync.Mutex
	var counter int
	nextPID := func() string {
		mu.Lock()
		defer mu.Unlock()
		counter++
		return fmt.Sprintf("secret-%d", counter)
	}

	e.Tool("fetch_with_password",
		"Fetch a URL that requires a password. The password is collected directly from the user and never exposed to the model.",
		json.RawMessage(schema),
		func(args json.RawMessage) ext.ToolResult {
			var in struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return ext.TextErrorResult("invalid args: " + err.Error())
			}
			if strings.TrimSpace(in.URL) == "" {
				return ext.TextErrorResult("url is required")
			}

			pid := nextPID()

			type result struct {
				password string
				ok       bool
			}
			ch := make(chan result, 1)

			// input and inputMu are owned by the key handler goroutine.
			var inputMu sync.Mutex
			var input string

			render := func() {
				inputMu.Lock()
				masked := strings.Repeat("●", len([]rune(input)))
				inputMu.Unlock()
				e.RenderPanel(pid, "Password required",
					[]string{
						"  URL:       " + in.URL,
						"",
						"  Password:  " + masked + "▌",
					},
					"type password  enter confirm  esc cancel")
			}

			e.OnPanelKey(pid, func(key, text string) {
				inputMu.Lock()
				switch key {
				case "rune":
					input += text
					inputMu.Unlock()
					render()
					return
				case "backspace":
					if len(input) > 0 {
						r := []rune(input)
						input = string(r[:len(r)-1])
					}
					inputMu.Unlock()
					render()
					return
				case "enter":
					password := input
					inputMu.Unlock()
					e.ClosePanel(pid)
					ch <- result{password: password, ok: true}
					return
				case "esc":
					inputMu.Unlock()
					e.ClosePanel(pid)
					ch <- result{}
					return
				default:
					inputMu.Unlock()
				}
			}, func() {
				// Panel closed by host (user navigated away).
				select {
				case ch <- result{}:
				default:
				}
			})

			// Open the panel from inside the tool handler.
			e.OpenPanel(pid, "Password required",
				[]string{
					"  URL:       " + in.URL,
					"",
					"  Password:  ▌",
				},
				"type password  enter confirm  esc cancel")

			// Block until the user submits or cancels.
			r := <-ch
			if !r.ok {
				return ext.TextErrorResult("cancelled: user did not provide a password")
			}

			// Use the password directly here. It never leaves this
			// process and is never written to any frame or transcript.
			return doFetch(in.URL, r.password)
		})

	if err := e.Run(); err != nil {
		e.Logf("fatal: %v", err)
		os.Exit(1)
	}
}

// doFetch is a stub. Replace with a real http.Get + BasicAuth or
// token header in production use.
func doFetch(url, password string) ext.ToolResult {
	if password == "" {
		return ext.TextErrorResult("fetch failed: empty password")
	}
	// Demonstrate that we *have* the password without logging it.
	return ext.TextResult(fmt.Sprintf(
		"fetched %s successfully (password was %d characters, not shown)",
		url, len([]rune(password)),
	))
}
