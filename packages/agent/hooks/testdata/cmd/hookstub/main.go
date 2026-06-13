// Command hookstub is a deterministic test hook. Behavior is picked
// by its first argument so one compiled binary covers every test
// case (a compiled Go binary sidesteps shell/python portability on
// Windows CI, same reasoning as the extensions echostub):
//
//	allow    -> {"decision":"allow"}
//	deny     -> {"decision":"deny","reason":"stub says no"}
//	ask      -> {"decision":"ask"}
//	rewrite  -> {"decision":"","updated_args":{"command":"echo rewritten"}}
//	exit2    -> stderr "blocked by exit2 stub", exit status 2
//	fail     -> exit status 1 (no opinion)
//	garbage  -> prints non-JSON
//	silent   -> prints nothing
//	echo     -> appends the stdin payload to the file named by arg 2
//	           (post-hook observation)
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "allow":
		fmt.Println(`{"decision":"allow"}`)
	case "deny":
		fmt.Println(`{"decision":"deny","reason":"stub says no"}`)
	case "ask":
		fmt.Println(`{"decision":"ask"}`)
	case "rewrite":
		fmt.Println(`{"updated_args":{"command":"echo rewritten"}}`)
	case "exit2":
		fmt.Fprintln(os.Stderr, "blocked by exit2 stub")
		os.Exit(2)
	case "fail":
		os.Exit(1)
	case "garbage":
		fmt.Println("this is not json")
	case "silent":
	case "echo":
		if len(os.Args) > 2 {
			in, _ := io.ReadAll(os.Stdin)
			f, err := os.OpenFile(os.Args[2], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				f.Write(append(in, '\n'))
				f.Close()
			}
		}
	}
}
