// hello — a tiny terva extension that registers /hello and /summon.
//
// Build it:
//
//	cd examples/extensions/hello
//	go build -o hello .
//
// Then drop it next to its extension.json under
// $TERVA_HOME/extensions/hello/, or run `terva ext install ./hello`
// from this directory.
package main

import (
	"strings"

	"terva.sh/terva/packages/agent/ext"
)

func main() {
	e := ext.New("hello", "1.0.0")

	// /hello [name] — submits a friendly prompt to the agent.
	e.Command("hello", "say hello (optional name)", func(args string) ext.Response {
		who := strings.TrimSpace(args)
		if who == "" {
			return ext.Prompt("Greet me with a short, slightly absurd compliment.")
		}
		return ext.Prompt("Greet " + who + " with a short, slightly absurd compliment.")
	})

	// /summon — pushes a notice into the chat without involving the
	// model. Useful for pretending we did something important.
	e.Command("summon", "show a tongue-in-cheek summon notice", func(args string) ext.Response {
		e.Notify("info", "the daemon stirs in its cage.")
		return ext.Display("a wisp of incense curls past your terminal.")
	})

	if err := e.Run(); err != nil {
		e.Logf("fatal: %v", err)
	}
}
