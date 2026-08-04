package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/discord"
	"terva.sh/terva/packages/agent/modes/telegram"
	"terva.sh/terva/packages/agent/secretadmin"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/secretstore"
)

// runSecretCommand dispatches `terva secret`: management of the at-rest
// encryption key and the migration of secret material onto it. Returns
// (handled=true, err) when rawArgs starts with "secret"; otherwise
// (false, nil) so the router falls through. Mirrors runDoctorCommand's shape.
//
// Everything here is shape-only on output: paths, modes, and the (public)
// recipient are printed; no secret value ever is. See
// docs/proposals/secrets-at-rest.md for the design.
func runSecretCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "secret" {
		return false, nil
	}
	rest := rawArgs[1:]
	if len(rest) == 0 {
		printSecretHelp()
		return true, nil
	}
	switch rest[0] {
	case "-h", "--help", "help":
		printSecretHelp()
		return true, nil
	case "init":
		return true, runSecretInit(os.Stdout)
	case "status":
		return true, runSecretStatus(os.Stdout)
	case "migrate":
		return true, runSecretMigrate(os.Stdout)
	case "encrypt":
		return true, runSecretEncrypt(rest[1:], os.Stdin, os.Stdout)
	case "rotate":
		return true, runSecretRotate(rest[1:], os.Stdout)
	case "list":
		return true, runSecretList(os.Stdout)
	case "grant":
		return true, runSecretGrant(rest[1:], os.Stdout)
	case "revoke":
		return true, runSecretRevoke(rest[1:], os.Stdout)
	case "forget":
		return true, runSecretForget(rest[1:], os.Stdout)
	case "web-token":
		return true, runSecretWebToken(rest[1:], os.Stdout)
	default:
		printSecretHelp()
		return true, i18n.Errorf("unknown subcommand for `secret`: %s", rest[0])
	}
}

func printSecretHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.secret", `terva secret — manage terva's secrets: encryption at rest, and the web token

usage:
  terva secret init     generate the encryption key and encrypt existing secrets
  terva secret status   report what is encrypted, what is still plaintext
  terva secret migrate  encrypt existing plaintext secrets with the configured key
  terva secret encrypt PATH
                        read a value on stdin, print its encrypted form
  terva secret rotate   replace the key; the old one is retired and can still
                        open files until they are next written
  terva secret rotate --revoke
                        replace the key and DESTROY the old one, re-encrypting
                        everything now — for a key that leaked
  terva secret list     name the stored scopes and their key NAMES (never values)
  terva secret grant PRINCIPAL SCOPE use|read [--ttl 720h]
                        let PRINCIPAL reach SCOPE; 'use' may ask terva to act
                        with the secret, 'read' may receive the material
  terva secret revoke PRINCIPAL SCOPE
                        withdraw one grant
  terva secret forget SCOPE [--purge]
                        drop terva's record of a component: its registry entry
                        and its grants. --purge also DELETES its stored values
  terva secret web-token init|rotate|path
                        issue the bearer token that gates 'terva web'

The key is an age X25519 identity. init writes it to the credential home as
secrets.key (owner-only) and records its PUBLIC half in config.json; supply
your own instead with --secrets-key-file, TERVA_SECRETS_KEY_FILE (a path), or
TERVA_SECRETS_KEY (the identity itself; scrubbed from the environment after
reading). Encrypted files are unrecoverable without the key — back it up.
Losing it means logging in and re-entering extension secrets again.

encrypt needs only the PUBLIC recipient from config.json, so a machine that
does not hold the key can still prepare a value to paste into config.json. It
takes the path the value will live at, because a sealed value is BOUND to that
path and refuses to open anywhere else — moving one somewhere it would be
handed to a different consumer is the attack that binding exists to stop:

  printf %s "$TOKEN" | terva secret encrypt extensions.weather.api_key`))
}

// runSecretInit generates the key, records the recipient, and migrates
// existing secret material. Refuses to touch an already-configured key: a
// second init that replaced the key would strand everything the first one
// encrypted.
func runSecretInit(w io.Writer) error {
	// An already-configured home is not an error, it is the goal. Since a fresh
	// install now sets itself up (initSecretsForFreshHome), the first thing a
	// user following the docs would hit is this path, and refusing there reads
	// as "you did something wrong" when nothing is wrong. init means "make sure
	// encryption is on and everything is sealed" — replacing a key is `rotate`,
	// which is a different and destructive verb.
	if _, err := config.SecretsIdentity(); err == nil {
		fmt.Fprintf(w, "encryption is already set up (key: %s)\n", config.SecretsKeyPath())
		if err := migrateAuthFile(w); err != nil {
			return err
		}
		if err := migrateConfigSecrets(w); err != nil {
			return err
		}
		return migrateChatTokens(w)
	} else if !errors.Is(err, secrets.ErrNoKey) {
		return fmt.Errorf("a secrets key is configured but unusable; fix or remove it first: %w", err)
	}
	// No key resolves — but encryption may still be ESTABLISHED, with the key
	// merely absent (not restored from backup, a --secrets-key-file that was
	// not passed, a home restored without it). Minting a fresh key here would
	// strand every file the old one encrypted, permanently and silently. The
	// evidence is on disk: a recorded recipient, or ciphertext in auth.json.
	if why := secretadmin.EstablishedEncryptionWithoutKey(); why != "" {
		return i18n.Errorf("encryption is already set up (%s) but its key is missing from %s — restore the key from backup, or point --secrets-key-file / %s at it. Refusing to generate a new key: it would make the existing encrypted material unrecoverable",
			why, config.SecretsKeyPath(), config.SecretsKeyFileEnv)
	}

	id, err := secrets.GenerateIdentity()
	if err != nil {
		return err
	}
	keyPath := config.SecretsKeyPath()
	if err := writeSecretsKey(keyPath, id); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s (owner-only)\n", keyPath)

	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: id.Recipient().String()}
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "recorded public recipient in config.json: %s\n", id.Recipient())

	if err := migrateAuthFile(w); err != nil {
		return err
	}
	if err := migrateConfigSecrets(w); err != nil {
		return err
	}
	if err := migrateChatTokens(w); err != nil {
		return err
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Back up the key file: everything it encrypts is unrecoverable without it.")
	return nil
}

// runSecretEncrypt seals one value read from stdin and prints the result, for
// pasting into config.json by hand. It needs only the public recipient, so it
// works on a host that never holds the key.
func runSecretEncrypt(args []string, r io.Reader, w io.Writer) error {
	// The destination is required, not optional: a sealed value is BOUND to the
	// path it will live at, and refuses to open anywhere else. Guessing a
	// default here would mint values that fail at the point of use, which is
	// the worst place to discover a typo.
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return i18n.Errorf("`terva secret encrypt` needs the config path the value will be stored at, e.g. extensions.weather.api_key — the sealed value is bound to it and will not open elsewhere")
	}
	path := strings.TrimSpace(args[0])
	in, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	// Trim only the trailing newline a shell adds; a secret may legitimately
	// contain spaces, and trimming those would corrupt it silently.
	value := strings.TrimRight(string(in), "\r\n")
	if value == "" {
		return i18n.Errorf("nothing on stdin to encrypt — pipe the value in, e.g. `printf ... | terva secret encrypt extensions.weather.api_key`")
	}
	sealed, err := config.EncryptFieldValue(path, value)
	if err != nil {
		if errors.Is(err, secrets.ErrNoKey) {
			return i18n.Errorf("no secrets key or recipient configured — run `terva secret init` first")
		}
		return err
	}
	fmt.Fprintln(w, sealed)
	return nil
}

// runSecretRotate replaces the at-rest key, moving everything it protects onto
// the new one.
//
// The ordering is the design, because a rotation that dies halfway must not
// take the credentials with it. Every file is first sealed to BOTH keys, then
// the key file is swapped, then everything is re-sealed to the new key alone.
// At no point on that path does the key sitting on disk fail to open the files
// beside it, so an interruption costs a re-run — never a re-login. The whole
// thing is also verified up front: a value that cannot be opened aborts the
// rotation before a single byte is written.
func runSecretRotate(args []string, w io.Writer) error {
	revoke := false
	for _, a := range args {
		switch a {
		case "--revoke":
			revoke = true
		default:
			return i18n.Errorf("unknown flag for `terva secret rotate`: %s (only --revoke)", a)
		}
	}
	old, err := config.SecretsIdentity()
	if err != nil {
		if errors.Is(err, secrets.ErrNoKey) {
			return i18n.Errorf("no secrets key configured — nothing to rotate (`terva secret init` sets one up)")
		}
		return err
	}
	if !revoke {
		return rotateLazy(old, w)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Open with the whole ring, not just the active key: a previous lazy
	// rotation may have left files sealed to a retired one, and --revoke has to
	// re-seal those too or it would strand exactly what it is meant to rescue.
	opening, err := openingRing(old)
	if err != nil {
		return err
	}

	// Verify BEFORE writing anything.
	if bad := build.VerifyConfigSecrets(cwd, opening); len(bad) > 0 {
		lines := make([]string, 0, len(bad))
		for _, b := range bad {
			lines = append(lines, b.Path+" ("+b.Reason+")")
		}
		return i18n.Errorf("refusing to rotate: %d config value(s) cannot be opened — %s",
			len(bad), strings.Join(lines, "; "))
	}
	authEncrypted := fileEncryptionState(config.AuthPath()) == stateEncrypted
	if authEncrypted {
		if _, err := authStoreOpening(opening).Load(); err != nil {
			return fmt.Errorf("refusing to rotate: %s cannot be opened with the current key: %w", config.AuthPath(), err)
		}
	}

	next, err := secrets.GenerateIdentity()
	if err != nil {
		return err
	}
	both := []age.Recipient{old.Recipient(), next.Recipient()}
	onlyNew := []age.Recipient{next.Recipient()}

	// Pass A — readable by either key, so the swap below is safe to interrupt.
	if err := resealAll(cwd, opening, both, authEncrypted); err != nil {
		return fmt.Errorf("rotate (phase 1, nothing lost — the old key still opens everything): %w", err)
	}

	// The swap. From here the new key is the one on disk, and it already
	// opens every file written above.
	keyPath := config.SecretsKeyPath()
	if err := writeSecretsKey(keyPath, next); err != nil {
		return fmt.Errorf("rotate (phase 2, the old key still opens everything — re-run to retry): %w", err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: next.Recipient().String()}
	}); err != nil {
		return fmt.Errorf("rotate: new key is installed at %s but config.json still names the old recipient — re-run: %w", keyPath, err)
	}

	// Pass B — drop the old key's access.
	if err := resealAll(cwd, next, onlyNew, authEncrypted); err != nil {
		return fmt.Errorf("rotate (phase 3): the new key is installed and works, but some values still ALSO open with the old key — re-run to finish: %w", err)
	}

	// Destroy the ring. --revoke means the old key is GONE, and a retired copy
	// that still opens things is exactly the thing being revoked. This runs
	// after the re-seal, so anything the ring could open has already moved.
	if err := os.Remove(config.RetiredSecretsKeyPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate: everything is on the new key, but the retired keys at %s could not be destroyed — remove the file: %w",
			config.RetiredSecretsKeyPath(), err)
	}

	fmt.Fprintf(w, "rotated: %s now holds a new key\n", keyPath)
	fmt.Fprintf(w, "recipient: %s\n", next.Recipient())
	fmt.Fprintln(w, "The previous key no longer opens anything — replace your backup of it.")
	// Warn at the moment the loss happens, not only in a status anyone might
	// run later. An unregistered component's file was NOT re-sealed, so terva
	// can no longer open it — the component itself keeps working, because it
	// opens with its own key, which is why this is a warning and not a refusal:
	// blocking a leak response over a connector nobody has started would get
	// the priority exactly backwards.
	for _, dir := range secretadmin.UnregisteredSealedComponents() {
		fmt.Fprintf(w, "warning: %s was not re-sealed — it never registered a recipient, so terva can no longer open it. It keeps working; start it once and re-run to restore access.\n", dir)
	}
	return nil
}

// registeredComponents is the registry contents, or none when it cannot be
// read — a reporting path must not fail the operation it is reporting on.
func registeredComponents() []secretstore.Component {
	cs, err := secretstore.NewRegistry(config.TervaHome()).List()
	if err != nil {
		return nil
	}
	return cs
}

// resealAll re-seals auth.json, secrets.json, and config.json's encrypted
// values, opening with `open` and sealing to `to`.
func resealAll(cwd string, open age.Identity, to []age.Recipient, includeAuth bool) error {
	// The secret store is whole-file sealed, so one re-seal moves every scope
	// at once — which is the reason it is one file rather than one per scope.
	// A store that is absent or still plaintext is left alone by Reseal.
	if err := config.SecretStoreIn(config.TervaHome()).Reseal(open, to...); err != nil {
		return fmt.Errorf("%s: %w", config.SecretStorePath(config.TervaHome()), err)
	}
	if err := resealComponents(open, to); err != nil {
		return err
	}
	if includeAuth {
		store := auth.NewStoreWithCodec(config.AuthPath(), secrets.NewRotationCodec(open, to...))
		// A no-op mutation re-writes what it read, through the file lock that
		// orders this against another terva refreshing a token right now.
		if err := store.Mutate(func(*auth.Credentials) {}); err != nil {
			return fmt.Errorf("auth.json: %w", err)
		}
	}
	if _, err := build.ResealConfigSecrets(cwd, open, to...); err != nil {
		return err
	}
	return nil
}

// resealComponents re-seals every registered component's file, opening with
// `open` and sealing to `to` PLUS the component's own recipient.
//
// Keeping the component's recipient is the whole point: re-sealing to terva's
// new key alone would lock the component out of its own state, which is the one
// outcome dual-recipient exists to prevent. That is also why the registry must
// know the recipient, and why the handshake is the only place it may come from.
//
// A component whose file is absent or holds nothing sealed is skipped. A
// component that CANNOT be opened is a hard failure: rotation refuses rather
// than leaving a file only the retired key can read.
func resealComponents(open age.Identity, to []age.Recipient) error {
	home := config.TervaHome()
	components, err := secretstore.NewRegistry(home).List()
	if err != nil {
		return fmt.Errorf("read the component registry: %w", err)
	}
	for _, c := range components {
		path := filepath.Join(home, c.File)
		doc, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", c.File, err)
		}
		own, err := secrets.ParseRecipient(c.Recipient)
		if err != nil {
			return fmt.Errorf("%s: registered recipient is unusable, run `terva secret forget %s`: %w", c.File, c.Scope(), err)
		}
		env := secrets.Envelope{Scope: c.Scope(), Paths: c.Paths}
		plain, err := env.Open(doc, open)
		if err != nil {
			return fmt.Errorf("%s: %w", c.File, err)
		}
		sealed, err := env.Seal(plain, append([]age.Recipient{own}, to...)...)
		if err != nil {
			return fmt.Errorf("%s: %w", c.File, err)
		}
		if err := privfs.WriteFile(path, sealed); err != nil {
			return fmt.Errorf("%s: %w", c.File, err)
		}
	}
	return nil
}

// authStoreOpening is a Store that reads auth.json with one specific identity,
// for the pre-flight check.
func authStoreOpening(id age.Identity) *auth.Store {
	return auth.NewStoreWithCodec(config.AuthPath(), secrets.NewOpeningCodec(id))
}

// openingRing is the active key plus every retired one, as a single identity —
// what --revoke must open with, since a previous lazy rotation may have left
// files on a retired key.
func openingRing(active *age.X25519Identity) (age.Identity, error) {
	retired, err := config.RetiredSecretsIdentities()
	if err != nil {
		return nil, fmt.Errorf("read retired keys: %w", err)
	}
	ids := []age.Identity{active}
	for _, r := range retired {
		ids = append(ids, r)
	}
	return secrets.Ring(ids...), nil
}

// rotateLazy is the HYGIENE rotation: mint a new active key, retire the old one
// into the ring, and rewrite nothing.
//
// Nothing is re-sealed because nothing has to be: every existing file still
// opens, through the retired key, and heals to the new one on its next write.
// That is the whole difference from --revoke, and it is what makes this
// survivable for a file terva does not own — a component that was offline
// across the rotation is not locked out of its own state.
//
// It also means there is no verification step here. Verification exists to stop
// a HALF-rotated home, and only the eager path can produce one; adding a key
// cannot strand a value that was already readable, and cannot rescue one that
// was not.
func rotateLazy(old *age.X25519Identity, w io.Writer) error {
	next, err := secrets.GenerateIdentity()
	if err != nil {
		return err
	}
	if err := retireKey(old); err != nil {
		return fmt.Errorf("rotate: could not retire the current key, nothing changed: %w", err)
	}
	keyPath := config.SecretsKeyPath()
	if err := writeSecretsKey(keyPath, next); err != nil {
		return fmt.Errorf("rotate: the old key is in the ring but the new key was not installed — re-run: %w", err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Secrets = &config.SecretsConfig{Recipient: next.Recipient().String()}
	}); err != nil {
		return fmt.Errorf("rotate: new key is installed at %s but config.json still names the old recipient — re-run: %w", keyPath, err)
	}

	fmt.Fprintf(w, "rotated: %s now holds a new key\n", keyPath)
	fmt.Fprintf(w, "recipient: %s\n", next.Recipient())
	fmt.Fprintf(w, "The previous key moved to %s and can still OPEN existing files.\n", config.RetiredSecretsKeyPath())
	fmt.Fprintln(w, "Files heal onto the new key as they are written. Use `terva secret rotate --revoke` instead if the old key leaked.")
	return nil
}

// retireKey appends an identity to the ring, owner-only, skipping one already
// there — a re-run must not grow the file forever.
func retireKey(id *age.X25519Identity) error {
	existing, err := config.RetiredSecretsIdentities()
	if err != nil {
		return err
	}
	for _, cur := range existing {
		if cur.String() == id.String() {
			return nil
		}
	}
	path := config.RetiredSecretsKeyPath()
	var b strings.Builder
	b.WriteString("# terva retired secrets keys — these may only OPEN, never seal.\n")
	b.WriteString("# `terva secret rotate` adds one; `terva secret rotate --revoke` destroys them.\n")
	for _, cur := range existing {
		fmt.Fprintf(&b, "%s\n", cur)
	}
	fmt.Fprintf(&b, "%s\n", id)
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return privfs.WriteFile(path, []byte(b.String()))
}

// writeSecretsKey writes an identity to the key file, owner-only.
func writeSecretsKey(path string, id *age.X25519Identity) error {
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	content := fmt.Sprintf("# terva secrets key — encrypts secret material at rest (auth.json, config secrets)\n# created: %s\n# public key: %s\n%s\n",
		time.Now().UTC().Format(time.RFC3339), id.Recipient(), id)
	return privfs.WriteFile(path, []byte(content))
}

// migrateConfigSecrets seals the plaintext secrets config.json holds, and says
// what it could not classify: an extension whose manifest is gone has values
// no schema can sort into secret and not-secret.
func migrateConfigSecrets(w io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	changed, err := build.EncryptConfigSecrets(cwd)
	if err != nil {
		return fmt.Errorf("encrypt config secrets: %w", err)
	}
	switch len(changed) {
	case 0:
		fmt.Fprintln(w, "config.json: no plaintext secrets to encrypt")
	default:
		fmt.Fprintf(w, "config.json: encrypted %d value(s)\n", len(changed))
		for _, p := range changed {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if unknown := build.ConfigSecretsUnknown(cwd); len(unknown) > 0 {
		fmt.Fprintf(w, "  note: no manifest found for %s — their saved values could not be classified\n",
			strings.Join(unknown, ", "))
	}
	return nil
}

// runSecretStatus reports the encryption posture: key source, recipient,
// and per-file encrypted/plaintext state. Read-only, shape-only.
//
// It assembles the SAME struct the ctrlproto secrets group returns and renders
// it, rather than reading disk for itself. One producer is what keeps a panel
// and a terminal from disagreeing about whether a file is encrypted — the
// failure this surface can least afford.
func runSecretStatus(w io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	secretadmin.WriteStatus(w, secretadmin.Status(cwd))
	return nil
}

// runSecretList names the stored scopes and their key names.
func runSecretList(w io.Writer) error {
	res, err := secretadmin.List()
	if err != nil {
		return err
	}
	secretadmin.WriteList(w, res)
	return nil
}

// runSecretGrant authorizes a principal against a scope.
func runSecretGrant(args []string, w io.Writer) error {
	var pos []string
	p := ctrlproto.SecretsGrantParams{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ttl":
			if i+1 >= len(args) {
				return i18n.Errorf("--ttl needs a duration, e.g. --ttl 720h")
			}
			i++
			p.TTL = args[i]
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 3 {
		return i18n.Errorf("usage: terva secret grant PRINCIPAL SCOPE use|read [--ttl 720h]")
	}
	p.Principal, p.Scope, p.Mode = pos[0], pos[1], pos[2]
	if err := secretadmin.Grant(p); err != nil {
		return err
	}
	fmt.Fprintf(w, "granted %s -> %s (%s)\n", p.Principal, p.Scope, p.Mode)
	return nil
}

// runSecretRevoke withdraws one grant.
func runSecretRevoke(args []string, w io.Writer) error {
	if len(args) != 2 {
		return i18n.Errorf("usage: terva secret revoke PRINCIPAL SCOPE")
	}
	p := ctrlproto.SecretsRevokeParams{Principal: args[0], Scope: args[1]}
	if err := secretadmin.Revoke(p); err != nil {
		return err
	}
	fmt.Fprintf(w, "revoked %s -> %s\n", p.Principal, p.Scope)
	return nil
}

// runSecretForget drops terva's record of a component.
//
// The command `terva secret status` has told users to run for some time — it
// names it in the "registered recipient is unusable" hint — without it existing.
func runSecretForget(args []string, w io.Writer) error {
	p := ctrlproto.SecretsForgetParams{}
	for _, a := range args {
		switch a {
		case "--purge":
			p.Purge = true
		default:
			if p.Scope != "" {
				return i18n.Errorf("usage: terva secret forget SCOPE [--purge]")
			}
			p.Scope = a
		}
	}
	if p.Scope == "" {
		return i18n.Errorf("usage: terva secret forget SCOPE [--purge]")
	}
	res, err := secretadmin.Forget(p)
	if err != nil {
		return err
	}
	secretadmin.WriteForget(w, p.Scope, res)
	return nil
}

// runSecretMigrate encrypts existing plaintext secret material with the
// configured key, and records the recipient if init predates that field.
func runSecretMigrate(w io.Writer) error {
	id, err := config.SecretsIdentity()
	if err != nil {
		if errors.Is(err, secrets.ErrNoKey) {
			return i18n.Errorf("no secrets key configured — run `terva secret init` first (it migrates as part of setup)")
		}
		return err
	}
	if err := config.MutateConfig(func(c *config.Config) {
		if c.Secrets == nil || c.Secrets.Recipient == "" {
			c.Secrets = &config.SecretsConfig{Recipient: id.Recipient().String()}
		}
	}); err != nil {
		return err
	}
	if err := migrateAuthFile(w); err != nil {
		return err
	}
	if err := migrateConfigSecrets(w); err != nil {
		return err
	}
	return migrateChatTokens(w)
}

// migrateChatTokens moves a bot token still sitting in bot.json or
// discord.json into the secret store.
//
// Load-then-save is the whole mechanism: LoadConfig honours a legacy in-file
// token and SaveConfig writes it to the store and drops it from the file, so
// this reuses the exact path an ordinary reconfiguration takes rather than
// reimplementing the move.
//
// Without this, a legacy token converts only when the user next reconfigures
// the bot — which they have no reason to do — so it would sit in plaintext
// indefinitely on a home that had just been told it was encrypted. Found by
// running the binary against a pre-store home; the suite was green.
func migrateChatTokens(w io.Writer) error {
	home := config.TervaHome()
	for _, m := range []struct {
		file string
		load func(string) (bool, error)
	}{
		{"bot.json", func(h string) (bool, error) {
			c, err := telegram.LoadConfig(h)
			if err != nil || c.LegacyToken == "" {
				return false, err
			}
			return true, telegram.SaveConfig(h, c)
		}},
		{"discord.json", func(h string) (bool, error) {
			c, err := discord.LoadConfig(h)
			if err != nil || c.LegacyToken == "" {
				return false, err
			}
			return true, discord.SaveConfig(h, c)
		}},
	} {
		moved, err := m.load(home)
		if err != nil {
			return fmt.Errorf("%s: %w", m.file, err)
		}
		if moved {
			fmt.Fprintf(w, "%s: bot token moved into %s\n", m.file, secretstore.FileName)
		}
	}
	return nil
}

// migrateAuthFile re-saves auth.json through the codec-carrying store, which
// encrypts it. A no-op mutation is the whole mechanism: Store.Mutate always
// writes back what it loaded.
func migrateAuthFile(w io.Writer) error {
	path := config.AuthPath()
	switch fileEncryptionState(path) {
	case stateAbsent:
		fmt.Fprintf(w, "auth.json: absent — nothing to migrate (it will be born encrypted on first login)\n")
		return nil
	case stateEncrypted:
		fmt.Fprintf(w, "auth.json: already encrypted\n")
		return nil
	}
	if err := config.AuthStoreFor().Mutate(func(*auth.Credentials) {}); err != nil {
		return fmt.Errorf("encrypt %s: %w", path, err)
	}
	fmt.Fprintf(w, "auth.json: encrypted\n")
	return nil
}

// The file states, named locally because doctor and the migrations switch on
// them. They ARE the wire constants — aliased rather than re-declared, so the
// terminal and the panel cannot end up with two vocabularies for the same sniff.
const (
	stateAbsent    = ctrlproto.SecretsFileAbsent
	stateEncrypted = ctrlproto.SecretsFileEncrypted
	statePlaintext = ctrlproto.SecretsFilePlaintext
)

// fileEncryptionState sniffs a file's leading bytes: absent / encrypted /
// plaintext. One implementation, in secretadmin, shared with the status report.
func fileEncryptionState(path string) string { return secretadmin.FileState(path) }

// keyFileMode reports the key file's permission posture the way doctor
// reports directories: mode plus an owner-only verdict.
func keyFileMode(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		// The identity resolved but not from this path (env-supplied).
		return "(supplied via environment)"
	}
	mode := fi.Mode().Perm()
	if mode&0o077 != 0 {
		return fmt.Sprintf("%#o  group/other-accessible — run: chmod 600 %s", mode, path)
	}
	return fmt.Sprintf("%#o  owner-only", mode)
}
