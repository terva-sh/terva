package secretadmin

import (
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/secretstore"
)

// List names the scopes and the KEY NAMES under each.
//
// A key name is schema, not material: "bot_token" says what a slot is for and
// nothing about what is in it. That is the line this whole surface is drawn on,
// and it is why list can exist at all while a `get` never will.
func List() (ctrlproto.SecretsListResult, error) {
	store := config.SecretStoreIn(config.TervaHome())
	scopes, err := store.Scopes()
	if err != nil {
		return ctrlproto.SecretsListResult{}, err
	}
	var out ctrlproto.SecretsListResult
	for _, sc := range scopes {
		keys, err := store.Keys(sc)
		if err != nil {
			return ctrlproto.SecretsListResult{}, err
		}
		out.Scopes = append(out.Scopes, ctrlproto.SecretsScopeKeys{Scope: sc, Keys: keys})
	}
	return out, nil
}

// Grant authorizes a principal against a scope.
//
// TTL is a duration and the deadline is computed HERE, against the daemon's
// clock. A caller that supplied an absolute time would be asserting agreement
// about now with a machine it may not share a timezone — or an accurate
// clock — with.
func Grant(p ctrlproto.SecretsGrantParams) error {
	g := secretstore.Grant{
		Principal: strings.TrimSpace(p.Principal),
		Scope:     strings.TrimSpace(p.Scope),
		Mode:      secretstore.Mode(strings.TrimSpace(p.Mode)),
	}
	if ttl := strings.TrimSpace(p.TTL); ttl != "" {
		d, err := time.ParseDuration(ttl)
		if err != nil {
			return fmt.Errorf("ttl %q: %w", p.TTL, err)
		}
		if d <= 0 {
			return fmt.Errorf("ttl %q is not in the future", p.TTL)
		}
		g.Expires = time.Now().Add(d)
	}
	return config.SecretStoreIn(config.TervaHome()).Grant(g)
}

// Revoke withdraws one grant, named by both halves. A principal may hold grants
// on several scopes, and dropping all of them because one was named would be a
// surprise in the direction that matters.
func Revoke(p ctrlproto.SecretsRevokeParams) error {
	principal, scope := strings.TrimSpace(p.Principal), strings.TrimSpace(p.Scope)
	if principal == "" || scope == "" {
		return fmt.Errorf("revoke needs both a principal and a scope")
	}
	return config.SecretStoreIn(config.TervaHome()).Revoke(principal, scope)
}

// Forget drops terva's record of a component: its registry entry, every grant
// naming it, and — only with Purge — the values it stored.
//
// This is the explicit reaping §8.13 Q10 settled on. It is never automatic,
// because "not seen for N days" is indistinguishable from a seasonal connector,
// and it has teeth beyond tidying: an uninstalled component never acks a new
// generation, so it pins every retired key in the ring open forever. Forget is
// the unblock.
//
// The registry entry goes FIRST. If the purge failed afterwards the operator is
// left with values and no recipient — recoverable, and visible in status as an
// unregistered component. The other order leaves a registry entry claiming a
// recipient for values that no longer exist, which reads as healthy.
func Forget(p ctrlproto.SecretsForgetParams) (ctrlproto.SecretsForgetResult, error) {
	scope := strings.TrimSpace(p.Scope)
	if scope == "" {
		return ctrlproto.SecretsForgetResult{}, fmt.Errorf("forget needs a scope, e.g. conn:discord-ext")
	}
	var out ctrlproto.SecretsForgetResult
	kind, name, hasKind := strings.Cut(scope, ":")
	if hasKind && name != "" {
		reg := secretstore.NewRegistry(config.TervaHome())
		known, err := reg.List()
		if err != nil {
			return out, err
		}
		for _, c := range known {
			if c.Kind == kind && c.Name == name {
				out.Component = true
				break
			}
		}
		if out.Component {
			if err := reg.Forget(kind, name); err != nil {
				return out, err
			}
		}
	}
	res, err := config.SecretStoreIn(config.TervaHome()).Forget(scope, p.Purge)
	if err != nil {
		return out, err
	}
	out.Grants, out.Values, out.Remaining = res.Grants, res.Values, res.Remaining
	return out, nil
}
