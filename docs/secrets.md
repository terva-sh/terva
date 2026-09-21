# Secrets at rest and the web bearer token

How terva encrypts what it stores, and how to mint the token that lets
`terva web` be reached from anywhere but loopback.

Read this once, when you decide to encrypt a data directory or to expose the
control panel. **A new install encrypts itself and generates a key you must back
up**, which is the one fact on this page that cannot wait until you need it.

For the data directory's layout, the flags, and everything else about invoking
terva, see [cli.md](cli.md).

## Secrets at rest (`terva secret`)

**A new install encrypts itself.** The first time terva starts against a data
directory that does not exist yet, it generates an
[age](https://age-encryption.org) X25519 identity at `secrets.key`, records its
public half in `config.json` (`secrets.recipient`), and says so on stderr, so
credentials written from then on are born encrypted. **Back that key up:
everything it encrypts is unrecoverable without it.**

An **existing** data directory is never converted behind your back, because
that step rewrites credentials that are already there and is the one that can
strand them. Turn it on with `terva secret init` (or `terva secret migrate`,
which is the same sweep once a key exists): it generates the identity, records
the recipient, and encrypts the secrets already on disk. Running `init` on a
directory that is already set up is a no-op that re-sweeps for anything
plaintext. Replacing a key is `terva secret rotate`, a different and
destructive verb.

Afterwards, reading those files yields ciphertext, and only terva's own
credential path decrypts it (in memory, at the moment of use).
`terva secret status` reports what is encrypted and what is still plaintext
without printing a value.

**Rotation comes in two kinds, because hygiene and a leak want opposite
things.**

- `terva secret rotate` is *hygiene.* A new key becomes the active one and the
  old key is retired to `secrets.keyring`, where it can still **open** files but
  never seals anything. Nothing is rewritten: each file heals onto the new key
  the next time it is written. Cheap, and safe to run on a schedule.
- `terva secret rotate --revoke` is for *the old key leaked.* Everything is
  re-encrypted onto the new key immediately and the retired keys are destroyed,
  so the old key opens nothing. It refuses up front if any value cannot be
  opened, rather than leaving a half-rotated directory.

Both are interruptible: at no point does the key on disk fail to open the files
beside it, so an interrupted rotation costs a re-run, never a re-login.

**Who may reach what.** The store's scopes default to deny: a principal reaches
the scope whose name matches its own and nothing else. `terva secret grant
PRINCIPAL SCOPE use|read [--ttl 720h]` opens one more. `use` means "may ask
terva to act with this secret, may not receive the material" and `read` means
the value itself; `terva secret revoke PRINCIPAL SCOPE` takes it back.
`terva secret list` names the scopes and their key names, never a value.

**`terva secret forget SCOPE`** drops terva's record of a component: its entry
in the recipient registry and every grant naming it. Nothing is ever reaped
automatically, because "not seen for N days" is indistinguishable from a
seasonal connector. Forgetting matters beyond tidiness too: an uninstalled
component never acks a new key generation, so it pins every retired key open
forever, and this is the unblock. It leaves the component's stored values in
place and says how many; `--purge` deletes those too.

The same report and the same grant management are available over
[ctrlproto](controllers.md#method-groups) as the optional `secrets` group, which
`terva web` serves only under `--web-allow-secrets`. Rotation is **not** on that
wire in any form: it supersedes or destroys a key, so it stays here, where a
human is at a terminal.

Three shapes, because the files are used differently:

- **`auth.json` is encrypted whole.** Nothing but terva reads it, so hiding
  the provider inventory costs nothing.
- **`secrets.json` is encrypted whole**, for the same reason. It holds the
  scoped secrets terva owns: today the Telegram and Discord bot tokens, which
  used to sit in the clear in `bot.json` and `discord.json` beside ordinary
  state like the bot username and poll offset. Only the credential moved; the
  rest of those files stays plaintext and inspectable. A token still sitting in
  the old place is reported by `terva secret status` and moved by
  `terva secret migrate`.
- **`config.json` is encrypted per value.** Only the secrets become
  `enc:age:v2:…` strings; structure, comments, and ordinary settings stay
  readable, so the file is still editable by hand, and by the agent. Covered:
  extension fields the manifest marks `secret`, and
  `image.backends.<id>.api_key`. To seal a value by hand, name the path it will
  live at and pipe the value in:

  ```
  printf %s "$TOKEN" | terva secret encrypt extensions.weather.api_key
  ```

  It needs only the public recipient, so it works on a machine that has no key.

  **A sealed value is bound to its path** and will not open anywhere else.
  That is deliberate: whoever can write `config.json` can also move a value
  they cannot read, and without binding they could park a provider key at a
  path whose consumer hands it somewhere visible. Move a value and it stops
  working; re-seal it at its new path instead. `terva secret migrate` upgrades
  values sealed by an older terva (`enc:age:v1:…`, unbound) in place.

MCP `env` and `headers` values are deliberately NOT encrypted. The sanctioned
way to keep a token out of that file is the existing `${ENV}` reference or
`auth.bearer_env`.

**The payoff: the agent may read a clean `config.json`.** It is normally on the
tool read deny-list because it carries credentials inline. Once every secret in
it is sealed, that denial lifts automatically, so you can ask the agent to help
with your own configuration. The check fails closed and re-runs each session:
one plaintext secret, one extension config block whose manifest is missing, or
one literal MCP `env`/`headers` value keeps the file denied, and `terva secret
status` (and `terva doctor`) name the exact reason. `auth.json` and
`secrets.key` stay denied unconditionally.

Supply your own key instead of generating one, following the web-token pattern:

| Source | Description |
|---|---|
| `--secrets-key-file <path>` | Read the key from a file you manage. Missing or unreadable is a startup error, never a silent fall back to plaintext. Pairs with systemd `LoadCredential=`. |
| `TERVA_SECRETS_KEY` (env) | The identity itself, for `EnvironmentFile=`/containers. Scrubbed from `os.Environ()` once read (the value still lingers in `/proc/<pid>/environ`, so prefer the file routes). |
| `TERVA_SECRETS_KEY_FILE` (env) | Path to the key file, when a flag is awkward (service managers, wrappers). |

The agent's read deny-list covers `secrets.key` wherever it resolves. **Back
the key up**: encrypted material is unrecoverable without it, and losing the key
means logging in again and re-entering extension secrets.

**Rotating the key.** `terva secret rotate` mints a new identity and moves
everything onto it. It verifies first, and a value it cannot open aborts the
rotation before anything is written. Then it seals every file to the old *and*
new key, swaps the key file, and re-seals to the new key alone. No point on
that path leaves the key on disk unable to open the files beside it, so an
interrupted rotation costs a re-run rather than your credentials. Afterwards
the previous key opens nothing; replace your backup.

## The web bearer token (`terva secret web-token`)

`terva web` is unauthenticated on loopback by default; a bearer token is what
lets it be reached from anywhere else (see [web.md](web.md)). terva can now
issue that token instead of you generating one out of band:

| Command | Description |
|---|---|
| `terva secret web-token init` | Mint a token into `$TERVA_HOME/web-token` (owner-only). Refuses if one exists. |
| `terva secret web-token rotate` | Replace it. A **running** daemon keeps accepting the old token until it restarts; every signed-in browser session is signed out once it does. |
| `terva secret web-token path` | Print the file's location. |

The value is printed **once**, at creation, and **only to a terminal**. Pipe
the command into anything else and you get the path instead, so the token
cannot land in a log or an agent transcript by accident. `cat` the file if you
need it again, or rotate if it is lost.

`terva web` and `terva attach` fall back to this file when no
`--web-token-file` / `--token-file` flag and no `TERVA_WEB_TOKEN` are set, so
minting a token is enough to turn authentication on at the next start, and a
local `terva attach` finds the same token with nothing to pass. An explicit
source always wins; an empty token file is a startup error rather than a
silent drop to no auth. The file is on the agent's read deny-list.
