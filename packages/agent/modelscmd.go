package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// modelsScaffold is the starter $TERVA_HOME/models.json written by
// `terva models init`. It is valid JSON (the loader uses strict
// encoding/json, so no comments are possible) and demonstrates the
// thing the docs call out as non-obvious: the openai-compatible
// provider stores ONE endpoint per /login, but models.json can carry
// as many endpoints as you like — each model entry pins its own
// baseUrl, so the two entries below point at two different servers and
// both show up in /model at once.
//
// Placeholder ids/urls are deliberately obvious so an unedited file
// fails loudly against a real server rather than silently "working".
const modelsScaffold = `{
  "providers": {
    "openai-compatible": {
      "models": [
        {
          "id": "REPLACE-with-a-model-id-your-server-serves",
          "name": "My Local Model",
          "reasoning": false,
          "contextWindow": 32768,
          "maxTokens": 4096,
          "baseUrl": "http://localhost:1234/v1",
          "capabilities": { "image-input": false }
        },
        {
          "id": "REPLACE-with-a-second-model-id",
          "name": "Second Endpoint",
          "reasoning": false,
          "contextWindow": 131072,
          "maxTokens": 8192,
          "baseUrl": "http://localhost:8000/v1"
        }
      ]
    }
  }
}
`

// runModelsCommand dispatches `terva models ...`. Returns (handled=true,
// err) if rawArgs starts with "models"; otherwise (handled=false, nil)
// so the main router falls through to the regular flag parser. Mirrors
// the dispatch shape of runUpdateCommand / runExtCommand so the router
// in cli.go stays uniform.
func runModelsCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "models" {
		return false, nil
	}
	rest := rawArgs[1:]
	if len(rest) == 0 {
		printModelsHelp()
		return true, nil
	}
	switch rest[0] {
	case "-h", "--help", "help":
		printModelsHelp()
		return true, nil
	case "init":
		force := false
		for _, a := range rest[1:] {
			switch a {
			case "-f", "--force":
				force = true
			case "-h", "--help":
				printModelsHelp()
				return true, nil
			default:
				printModelsHelp()
				return true, fmt.Errorf("unknown flag for `models init`: %s", a)
			}
		}
		return true, runModelsInit(force)
	default:
		printModelsHelp()
		return true, fmt.Errorf("unknown models subcommand: %s", rest[0])
	}
}

func printModelsHelp() {
	fmt.Fprintln(os.Stderr, `terva models — manage your custom model catalog ($TERVA_HOME/models.json)

usage:
  terva models init           scaffold a models.json you can edit
  terva models init --force    overwrite an existing models.json
  terva models help            show this help

notes:
  * models.json lets you register models that aren't in the built-in
    catalog, or pin exact context/max-token sizes for local and
    OpenAI-compatible endpoints. See docs/providers.md.
  * Each entry's "baseUrl" pins that model to a specific endpoint, so a
    single models.json can point at several servers at once — unlike
    '/login', which stores only one openai-compatible endpoint.
  * After editing, run 'terva --list-models' to confirm your entries load
    (they show source: user).`)
}

// runModelsInit writes the starter scaffold to $TERVA_HOME/models.json.
// It refuses to clobber an existing file unless force is set, so a user
// who already has a hand-tuned catalog can't lose it to a stray
// `terva models init`.
func runModelsInit(force bool) error {
	path := UserModelsPath()
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("models.json already exists at %s (pass --force to overwrite)", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(modelsScaffold), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("wrote %s\n", path)
	fmt.Println("  edit it to add your endpoints, then run `terva --list-models` to verify")
	return nil
}
