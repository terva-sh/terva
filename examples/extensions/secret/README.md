# secret — example terva extension

Demonstrates **secret collection via a masked panel**: the model asks
for a resource that needs a credential, the extension collects the
credential directly from the user inside a panel, uses it to perform
the operation, and returns only the outcome to the model. The secret
is never written to any JSON frame or the transcript.

## What it does

Registers one LLM-callable tool:

```
fetch_with_password(url: string)
```

When the model calls it, a masked password panel opens:

```
╭─ Password required ─────────────────────╮
│  URL:       https://internal.example.com │
│                                          │
│  Password:  ●●●●●●●▌                    │
╰──────────────────────────────────────────╯
  type password  enter confirm  esc cancel
```

The panel re-renders after every keystroke, showing bullets instead of
characters. Pressing Enter unblocks the tool goroutine, which uses the
password directly (in `doFetch`) and returns only the fetch outcome to
the model.

## Security property

The password lives only in the extension process's memory. It is never
serialised into a JSON frame, never appears in the transcript, and
never reaches the model. The model sees:

```
fetched https://internal.example.com successfully (password was 9 characters, not shown)
```

## Build

```bash
cd examples/extensions/secret
go build -o secret .
```

## Install

```bash
terva ext install .
```

## Try it

In terva, ask:

> Fetch https://internal.example.com/report — it needs a password.

The model calls `fetch_with_password`; the masked panel opens. Type
anything and press **Enter**. The model receives the result; the
password is gone.

## Adapting to real use

Replace `doFetch` in `main.go` with a real HTTP request:

```go
func doFetch(url, password string) ext.ToolResult {
    req, _ := http.NewRequest("GET", url, nil)
    req.SetBasicAuth("user", password)
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return ext.TextErrorResult("fetch failed: " + err.Error())
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    return ext.TextResult(string(body))
}
```

The same pattern works for any credential type: API tokens, SSH
passphrases, TOTP codes, or freeform override strings.

## See also

- `examples/extensions/approve` — same pattern for approve/deny gates
  (no text input, just y/n)
- `docs/extensions.md` — full protocol reference
