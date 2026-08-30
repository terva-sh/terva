package build

// Construction of the screening classifier from user config: deciding whether
// it runs at all, and resolving WHICH model does the screening.
//
// This lives in build because it needs credential resolution, the model
// catalogue and the swarm tier ladder, all of which build already owns. The
// classifier package itself takes a ready client, so it stays testable with no
// config and no network.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/classifier"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// InstallClassifier builds the screening classifier from the USER config and
// installs it on gate. It is the one-line form every host calls.
//
// It LOADS the config itself rather than accepting one, and that is the whole
// point of the signature: the classifier is user-layer only, so a caller must
// not be able to hand it a project-layered EffectiveConfig by accident. Making
// that impossible here is worth more than the flexibility it costs, because
// `approve` mode lets a model permit tool calls on the operator's behalf.
//
// override is the --classifier flag (Args.Classifier), or "" for none. It is a
// MODE STRING and deliberately not a config, which is what lets it coexist
// with the paragraph above: the user config is still the only config this
// function reads, and the override can do nothing but choose among the modes
// that config could already have named. argv is a user-layer source — a cloned
// repository cannot write it — so the trust boundary is unchanged.
//
// A nil warnf sends notes to stderr, matching HeadlessConfirmGate, which
// already announces the gate's stance there. Nothing is installed unless a
// classifier actually built, so every failure path leaves the gate untouched.
func InstallClassifier(gate *core.ConfirmGate, r Resolved, override string, warnf func(format string, args ...any)) {
	if gate == nil {
		return
	}
	if warnf == nil {
		warnf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "note: "+f+"\n", a...) }
	}
	cfg, _ := config.LoadConfig()
	if m := strings.TrimSpace(override); m != "" {
		// The flag wins over the config key, including "off": turning screening
		// off for one run is as legitimate as turning it on, and is the
		// cheapest way out when a screener starts refusing something it should
		// not. Only the MODE is overridden — provider, model, host_model and
		// timeout keep coming from config, so --classifier=screen on a machine
		// with no classifier block still resolves the cheap weak rung.
		cfg.Classifier.Mode = m
	}
	cls, clsMode := ClassifierFor(r, cfg, warnf)
	if cls == nil {
		return
	}
	gate.SetClassifier(cls, clsMode)

	// Say so, every time. The TUI has a status-bar tag for this, but a
	// headless run has no such surface, and "a model is now answering your
	// approvals" is not something to leave implicit anywhere — least of all in
	// approve mode, where nobody is asked at all.
	switch clsMode {
	case core.ClassifierApprove:
		warnf("%s", i18n.T("classifier: APPROVE mode — a model may permit tool calls on your behalf without asking"))
	case core.ClassifierScreen:
		warnf("%s", i18n.T("classifier: screen mode — a model may refuse tool calls; approvals still reach you"))
	}
}

// ClassifierFor builds the screening classifier described by cfg, or reports
// that none should run.
//
// It returns (nil, ClassifierOff) for every reason a classifier cannot run:
// switched off, an unparseable mode, no credential, an unresolvable model. The
// caller installs the pair with gate.SetClassifier and the gate then behaves
// exactly as it did before the feature existed — the prompt reaches a human.
//
// 🪤 Failing to OFF, loudly, is the deliberate choice at every branch here. The
// alternative — carrying on with some other model — is how a screening call
// silently becomes an expensive one, and this runs on gated calls for a whole
// session. warnf is where that noise goes; a caller that passes nil gets
// silence and deserves what it gets.
func ClassifierFor(r Resolved, cfg config.Config, warnf func(format string, args ...any)) (core.Classifier, core.ClassifierMode) {
	if warnf == nil {
		warnf = func(string, ...any) {}
	}

	clsMode, err := core.ParseClassifierMode(cfg.Classifier.Mode)
	if err != nil {
		warnf("%s", i18n.T("classifier: %v; screening stays off", err))
		return nil, core.ClassifierOff
	}
	if !clsMode.Enabled() {
		return nil, core.ClassifierOff
	}

	res, model, reasoning, err := classifierTarget(r, cfg)
	if err != nil {
		warnf("%s", i18n.T("classifier: %v; screening stays off", err))
		return nil, core.ClassifierOff
	}
	if !res.HasCredential() {
		warnf("%s", i18n.T("classifier: no credential for provider %q; screening stays off", res.Provider))
		return nil, core.ClassifierOff
	}

	timeout := time.Duration(cfg.Classifier.TimeoutMS) * time.Millisecond
	s := classifier.New(classifier.Options{
		Client:    res.NewClient(),
		Model:     model,
		Reasoning: reasoning,
		Timeout:   timeout,
		Logf:      warnf,
	})
	if s == nil {
		// Defensive: New refuses what it cannot run, and a typed-nil in the
		// interface would give the gate a classifier it thinks is live.
		warnf("%s", i18n.T("classifier: could not build a screener for %q/%q; screening stays off", res.Provider, model))
		return nil, core.ClassifierOff
	}
	return s, clsMode
}

// classifierTarget resolves which model screens, and it defaults to CHEAP.
//
// An expensive default would be silent and permanent: screening runs on gated
// calls for the whole session, and nobody reads a startup line twice. So with
// no explicit provider/model the weak rung of the swarm_tiers ladder wins —
// the same ladder swarm_spawn's `tier` uses, which composes the operator's
// overrides over terva's built-in family tables and already caps at the host's
// own strength. Most providers therefore answer with nothing configured, and
// `terva models tiers` prints exactly what will be picked.
func classifierTarget(r Resolved, cfg config.Config) (res Resolved, model, reasoning string, err error) {
	wantProv := strings.TrimSpace(cfg.Classifier.Provider)
	wantModel := strings.TrimSpace(cfg.Classifier.Model)

	// An explicit choice is honoured STRICTLY. Resolve substitutes rather
	// than failing (an unknown model falls back to the catalogue default),
	// which is right for an interactive session where a human reads the
	// warning once, and wrong for a screening path whose warning scrolls past
	// at startup and then bills the fallback all session.
	if wantProv != "" || wantModel != "" {
		prov := wantProv
		if prov == "" {
			prov = r.Provider
		}
		alt, aerr := Resolve(Args{Mode: mode.Print, Provider: prov, Model: wantModel, CWD: r.CWD}, true)
		if aerr != nil {
			return res, "", "", fmt.Errorf("resolving %s/%s: %w", prov, wantModel, aerr)
		}
		if wantModel != "" && !strings.EqualFold(alt.Model, wantModel) {
			return res, "", "", i18n.Errorf("asked for model %q but resolved %q; refusing to silently bill the fallback", wantModel, alt.Model)
		}
		if wantProv != "" && !strings.EqualFold(alt.Provider, wantProv) {
			return res, "", "", i18n.Errorf("asked for provider %q but resolved %q; refusing to silently use the fallback", wantProv, alt.Provider)
		}
		return alt, alt.Model, alt.Reasoning, nil
	}

	// A rung may name only an EFFORT ("the built-in model for this rung, but
	// think this hard"), which is how a provider with one good model and no
	// cheap sibling still gets a real ladder. No special case is needed for it
	// here: overridePick fills the model in from the built-in family table and
	// returns nothing at all when it cannot, so a resolved pick always carries
	// a Model and the effort rides along on Reasoning.
	pick := tools.ResolveSwarmTier(r.Provider, r.Model, "weak", SwarmTierMap(cfg.SwarmTiers))
	if pick.Model != "" {
		return r, pick.Model, pick.Reasoning, nil
	}

	// Gateways (opencode-go, OpenRouter, LiteLLM) have no built-in family
	// table, so nothing resolves. Falling back to the host model here would
	// rebuild the invisible per-call bill this ordering exists to prevent.
	if cfg.Classifier.HostModel {
		return r, r.Model, "", nil
	}
	// One literal, not a '+' concatenation: terva-i18n-lint extracts a T/Errorf
	// source only from a single string literal, and it skips a concatenation
	// silently, which would leave this line out of the catalog with no warning.
	return res, "", "", i18n.Errorf("no weak tier resolves for provider %q, so screening would run on the host model %q at host price per gated call; set swarm_tiers.%s.weak (see `terva models tiers`), or classifier.model, or accept the cost with classifier.host_model", r.Provider, r.Model, r.Provider)
}
