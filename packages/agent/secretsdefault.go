package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/secretadmin"
	"terva.sh/terva/packages/secrets"
)

// initSecretsForFreshHome turns at-rest encryption on for a home that has
// nothing in it yet, so a new install's credentials are born encrypted.
//
// Encryption is opt-in for existing installs and always will be — migrating
// somebody's home behind their back is how credentials get stranded. But a
// FRESH home has nothing to strand: at the moment of this decision there is no
// auth.json and no config.json, so the worst case is a key file the user did
// not ask for. That asymmetry is what makes default-on safe here and unsafe
// one step later.
//
// It also decides whether the feature does anything at all for most people.
// The config.json read-lift (build.ConfigReadableByAgent) only fires when
// encryption is on, so leaving this opt-in means the payoff lands solely for
// users who went looking for a flag.
//
// Every refusal below is silent and leaves the home untouched — this runs on
// every startup and must never be the reason terva does not start. The one
// thing it is NOT quiet about is success: a key nobody knows exists is a key
// nobody backs up.
func initSecretsForFreshHome() {
	// Project scoping splits the homes: config.json would be the project's
	// while the key belongs to the pinned global credential home. Recording a
	// recipient in a project file is not what anyone means, so leave it.
	if config.TervaHome() != config.CredentialHome() {
		return
	}
	if _, err := config.SecretsIdentity(); err == nil {
		return // a key is already configured, here or via flag/env
	} else if !errors.Is(err, secrets.ErrNoKey) {
		return // configured but broken: the user's to fix, not ours to paper over
	}
	// A key that merely went missing looks identical to a never-encrypted home
	// through "does a key resolve?" — and generating a new one there strands
	// everything the old one sealed. Same evidence check `terva secret init`
	// makes, for the same reason.
	if secretadmin.EstablishedEncryptionWithoutKey() != "" {
		return
	}
	// Fresh means there is nothing to migrate. ANY of these existing makes this
	// an EXISTING home, where turning encryption on is `terva secret migrate` —
	// an explicit act, because it rewrites material that is already there.
	//
	// bot.json and discord.json are on the list because they hold a credential:
	// a home with one is not new, and announcing "new install" over it is both
	// untrue and misleading about what just got protected (the token in there
	// does NOT move until migrate runs).
	home := config.TervaHome()
	for _, p := range []string{
		config.AuthPath(),
		config.ConfigPath(),
		filepath.Join(home, "bot.json"),
		filepath.Join(home, "discord.json"),
		config.SecretStorePath(home),
	} {
		if fileExists(p) {
			return
		}
	}

	id, err := secrets.GenerateIdentity()
	if err != nil {
		return
	}
	keyPath := config.SecretsKeyPath()
	if err := writeSecretsKey(keyPath, id); err != nil {
		return
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: id.Recipient().String()}
	}); err != nil {
		// The key exists but nothing records it. Remove it rather than leave a
		// half-configured home: on the next start this function would find no
		// recipient, no ciphertext, and no reason not to try again.
		_ = os.Remove(keyPath)
		return
	}
	fmt.Fprintf(os.Stderr,
		"note: new install — credentials will be encrypted at rest with %s\n"+
			"      Back it up: everything it encrypts is unrecoverable without it (`terva secret status`).\n",
		keyPath)
}
