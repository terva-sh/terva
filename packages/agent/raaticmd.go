package agent

// raaticmd wires `terva raati` — the CLI front door of the RAATI
// deliberation primitive (docs/proposals/raati-deliberation.md): a
// one-shot mode that convenes the panel over the swarm engine, prints
// the verdict with its minority report, persists the record, and
// exits. Level 0 (one binding shared by every unit) is the only rigor
// level wired so far; the panel inherits the invocation's resolved
// provider/model, so `terva raati --provider ollama --model X "q"` is
// the free local eight-ball.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/vote"
)

// raatiOpts are the raati-specific flags, extracted before ParseArgs
// (which rejects unknown flags) — the bot-command pattern.
type raatiOpts struct {
	class       raati.Class
	level       int
	evidence    []string
	timeout     time.Duration
	singleRound bool
	vetoHolder  string
	seatOrder   string // "" = user config, then the convene default
	profile     string // named convening bundle (user config raati.profiles)
	// classSet / levelSet distinguish an explicit flag from the
	// default, so a profile only fills what the invocation left unsaid.
	classSet bool
	levelSet bool
}

// runRaatiCommand dispatches `terva raati "question" [flags]`.
// Registered in Run's subcommand router; returns handled=false for
// any other argv.
func runRaatiCommand(rawArgs []string, _ string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "raati" {
		return false, nil
	}
	tail, opts, err := extractRaatiFlags(rawArgs[1:])
	if err != nil {
		return true, err
	}
	args, err := build.ParseArgs(tail)
	if err != nil {
		return true, err
	}
	if args.Help {
		printRaatiHelp()
		return true, nil
	}
	return true, raatiRun(args, opts)
}

// extractRaatiFlags pulls the raati-specific flags out of the raw arg
// tail (both `--flag v` and `--flag=v` forms). Unlike opt-in
// convenience flags, these are correctness knobs: a malformed value
// aborts with an error instead of being silently ignored.
func extractRaatiFlags(tail []string) (rest []string, o raatiOpts, err error) {
	o.class = raati.ClassAdvisory
	for i := 0; i < len(tail); i++ {
		a := tail[i]
		take := func(flag string) (string, bool) {
			if a == flag {
				if i+1 < len(tail) {
					i++
					return tail[i], true
				}
				return "", true
			}
			if v, ok := strings.CutPrefix(a, flag+"="); ok {
				return v, true
			}
			return "", false
		}
		if v, ok := take("--class"); ok {
			if o.class, err = raati.ParseClass(v); err != nil {
				return nil, o, err
			}
			o.classSet = true
			continue
		}
		if v, ok := take("--level"); ok {
			n, aerr := strconv.Atoi(strings.TrimSpace(v))
			if aerr != nil || n < 0 || n > 2 {
				return nil, o, i18n.Errorf("--level wants 0 (kaiku: one shared binding), 1 (kuoro: the provider's tier ladder), or 2 (käräjät: cross-provider seats from raati.level2); got %q", v)
			}
			o.level = n
			o.levelSet = true
			continue
		}
		if v, ok := take("--profile"); ok {
			if strings.TrimSpace(v) == "" {
				return nil, o, i18n.Errorf("--profile wants a name from the user config's raati.profiles")
			}
			o.profile = strings.TrimSpace(v)
			continue
		}
		if v, ok := take("--evidence"); ok {
			if strings.TrimSpace(v) == "" {
				return nil, o, i18n.Errorf("--evidence wants a file path")
			}
			o.evidence = append(o.evidence, v)
			continue
		}
		if v, ok := take("--round-timeout"); ok {
			d, derr := time.ParseDuration(strings.TrimSpace(v))
			if derr != nil || d <= 0 {
				return nil, o, i18n.Errorf("--round-timeout wants a positive duration, got %q", v)
			}
			o.timeout = d
			continue
		}
		if v, ok := take("--veto-holder"); ok {
			o.vetoHolder = strings.TrimSpace(v)
			continue
		}
		if v, ok := take("--seat-order"); ok {
			if _, serr := raati.ParseSeatOrder(v); serr != nil {
				return nil, o, serr
			}
			o.seatOrder = strings.TrimSpace(v)
			continue
		}
		if a == "--single-round" {
			o.singleRound = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, o, nil
}

func raatiRun(args build.Args, o raatiOpts) error {
	if note, perr := maybeEnableProjectScope(args); perr != nil {
		return perr
	} else if note != "" {
		fmt.Fprintln(os.Stderr, "terva:", note)
	}
	r, err := build.Resolve(args, true)
	if err != nil {
		return err
	}

	question := strings.TrimSpace(args.Prompt)
	if question == "" {
		piped, _ := readAllStdin()
		question = strings.TrimSpace(piped)
	}
	if question == "" {
		return i18n.Errorf("raati needs a question (arg or stdin); try: terva raati \"should we ship it?\"")
	}
	evidence, err := loadRaatiEvidence(o.evidence)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Seat the rigor level: every seat is an exact pin (children left
	// unpinned would resolve their OWN defaults and quietly ignore a
	// --provider/--model given here). The pool maps onto seats per the
	// seat order: flag, else user config, else the shuffled-per-convene
	// default.
	uc, _ := config.LoadConfig()
	// A profile fills what the invocation left unsaid; explicit flags
	// win. Seats are the exception: pinned seats replace raati.level2
	// and no flag can (the human has --provider/--model for level 0).
	var prof raati.Profile
	// Whether the level came from a profile's "auto" rather than a flag —
	// the gate honesty check below only refuses auto resolutions.
	levelViaAuto := false
	if o.profile != "" {
		if prof, err = raati.ProfileFor(build.RaatiProfiles(uc), o.profile); err != nil {
			return err
		}
		if !o.classSet && prof.Class != "" {
			if o.class, err = raati.ParseClass(prof.Class); err != nil {
				return i18n.Errorf("profile %q: %s", o.profile, err.Error())
			}
		}
		if !o.levelSet {
			if lvl, ok, auto := prof.PickLevel(tools.HighestRaatiLevel(r.Provider, build.SwarmTierMap(uc.SwarmTiers), build.RaatiLevel2Bindings(uc), len(raati.DefaultPanel()))); ok {
				o.level, levelViaAuto = lvl, auto
			}
		}
		if !o.singleRound && prof.SingleRound != nil {
			o.singleRound = *prof.SingleRound
		}
		if o.seatOrder == "" {
			o.seatOrder = prof.SeatOrder
		}
		if prof.Inquire != "" {
			// Honesty over silence: the CLI convene has no clerk wired
			// yet, so a profile's inquiry gap cannot run here.
			fmt.Fprintln(os.Stderr, "terva:", i18n.T("profile %q sets inquire — the CLI convene has no clerk yet; proceeding without the inquiry gap", o.profile))
		}
	}
	level2 := build.RaatiLevel2Bindings(uc)
	if len(prof.Seats) > 0 {
		if err := prof.ValidSeats(len(raati.DefaultPanel())); err != nil {
			return i18n.Errorf("profile %q: %s", o.profile, err.Error())
		}
		level2 = prof.Seats
	}
	// The CLI convene runs standalone — there is no live session whose
	// provider cache panel traffic could evict — so raati.spare_host does
	// not apply here and the ladder stays on the resolved provider.
	pool, err := tools.ResolveRaatiBindings(o.level, r.Provider, r.Model, r.Provider, build.SwarmTierMap(uc.SwarmTiers), level2, len(raati.DefaultPanel()))
	if err != nil {
		return err
	}
	// The gate honesty check reads the resolved SEATS: a ladder that is one
	// model at several thinking efforts is a real advisory panel and not
	// three independent judges.
	if rerr := tools.RefuseCorrelatedGate(o.profile, o.class, pool, levelViaAuto); rerr != nil {
		return rerr
	}
	seatOrderRaw := o.seatOrder
	if seatOrderRaw == "" {
		seatOrderRaw = uc.Raati.SeatOrder
	}
	seatOrder, err := raati.SeatOrderFor(seatOrderRaw, pool)
	if err != nil {
		return err
	}
	seatMap := uc.Raati.SeatMap
	if len(prof.SeatMap) > 0 {
		seatMap = prof.SeatMap
	}
	maxRounds := 0
	if prof.Converge != nil && *prof.Converge {
		maxRounds = 3
	}

	eng := swarm.New(swarm.Config{Root: swarm.DefaultRoot(config.TervaHome()), RepoRoot: r.CWD})
	// Units are daemons; Convene dismisses its own seats, and this
	// bounds the drain for anything a cancelled run left behind.
	defer eng.StopAllAndWait(5 * time.Second)

	fmt.Fprintln(os.Stderr, i18n.T("RAATI convened — class %s, level %d (%s), seats %s", string(o.class), o.level, tools.RaatiLevelName(o.level), string(seatOrder)))
	res, err := raati.Convene(ctx, raati.Config{
		Engine:       raati.SwarmEngine{Swarm: eng},
		Class:        o.class,
		VetoHolder:   o.vetoHolder,
		Bindings:     pool,
		SeatOrder:    seatOrder,
		SeatMap:      seatMap,
		RoundTimeout: o.timeout,
		SingleRound:  o.singleRound,
		MaxRounds:    maxRounds,
		Progress:     func(msg string) { fmt.Fprintln(os.Stderr, "  "+msg) },
	}, question, evidence)
	if err != nil {
		return err
	}

	printRaatiSeats(os.Stdout, res)
	printRaatiResult(os.Stdout, res)
	if path, werr := build.WriteRaatiRecord(res); werr != nil {
		fmt.Fprintln(os.Stderr, "terva:", i18n.T("could not persist the deliberation record: %s", werr.Error()))
	} else {
		fmt.Fprintln(os.Stdout, i18n.T("record: %s", path))
	}
	return nil
}

// loadRaatiEvidence assembles the evidence bundle the convener
// submits to the panel. Bounded per the proposal's honesty rule: what
// gets truncated is SAID to be truncated, never silently dropped.
func loadRaatiEvidence(paths []string) (string, error) {
	const perFileCap = 24 * 1024
	var sb strings.Builder
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", i18n.Errorf("evidence %s: %s", p, err.Error())
		}
		body := string(raw)
		truncated := false
		if len(body) > perFileCap {
			body = body[:perFileCap]
			truncated = true
		}
		fmt.Fprintf(&sb, "--- %s ---\n%s\n", p, body)
		if truncated {
			sb.WriteString(i18n.P("raati.evidence.truncated", "[evidence truncated at 24KiB — the panel judges what it was shown]"))
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

// printRaatiSeats prints one compact line naming each seat's binding —
// which model held which prior is part of the verdict's meaning.
func printRaatiSeats(w io.Writer, res *raati.Result) {
	var parts []string
	for _, u := range res.Units {
		if u.Provider == "" && u.Model == "" {
			return // unpinned panel (shouldn't happen via the CLI); say nothing
		}
		parts = append(parts, fmt.Sprintf("%s=%s/%s", u.Name, u.Provider, u.Model))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("seats: %s", strings.Join(parts, "  ")))
}

func printRaatiResult(w io.Writer, res *raati.Result) {
	fmt.Fprintln(w)
	verdict := strings.ToUpper(string(res.Outcome.Decision))
	if res.Outcome.Degraded {
		fmt.Fprintln(w, i18n.T("verdict: %s (degraded — not every unit voted)", verdict))
	} else {
		fmt.Fprintln(w, i18n.T("verdict: %s", verdict))
	}
	t := res.Outcome.Tally
	fmt.Fprintln(w, i18n.T("tally: %d approve / %d reject / %d abstain / %d absent — rule: %s", t.Approve, t.Reject, t.Abstain, t.Absent, res.Outcome.Rule))
	fmt.Fprintln(w)
	for _, b := range res.Final {
		if b.Absent {
			fmt.Fprintf(w, "  %-12s %s\n", b.Unit, i18n.T("absent — %s", b.Rationale))
			continue
		}
		fmt.Fprintf(w, "  %-12s %-8s %.2f  %s\n", b.Unit, string(b.Verdict), b.Confidence, b.Rationale)
	}
	if len(res.Outcome.Minority) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, i18n.T("minority report:"))
		for _, b := range res.Outcome.Minority {
			fmt.Fprintf(w, "  %s: %s\n", b.Unit, b.Rationale)
		}
	}
	if res.Outcome.Decision == vote.Escalated {
		fmt.Fprintln(w)
		fmt.Fprintln(w, i18n.T("the panel could not decide — this question wants a human"))
	}
	fmt.Fprintln(w)
}

func printRaatiHelp() {
	fmt.Fprint(os.Stderr, i18n.H("help.raati", `terva raati — convene a three-unit deliberation panel on a question

  terva raati "should we ship it?" [flags]
  git diff | terva raati --evidence CHANGELOG.md "ready to release?"

The panel (YATA-1 truth / KUSANAGI-2 decisiveness / MAGATAMA-3
benevolence) deliberates blind, cross-examines, and votes. The verdict
prints with the tally and the minority report — a split is information,
not failure. The full record is persisted under $TERVA_HOME/raati/.

flags:
  --class advisory|gate|veto   decision rule (default advisory majority;
                               gate = unanimity, fails closed)
  --level 0|1|2                rigor level: 0 kaiku (all seats on this
                               invocation's binding — correlated, triage
                               only), 1 kuoro (the provider's weak/medium/
                               strong ladder; needs a full ladder, see
                               "terva models tiers"), 2 käräjät (cross-
                               provider seats from the user config's
                               raati.level2 — real decorrelation,
                               gate-grade)
  --evidence PATH              submit a file to the panel (repeatable)
  --round-timeout DUR          per-round deadline; a late unit abstains
                               (default 5m)
  --single-round               blind ballots are final (the eight-ball)
  --veto-holder NAME           blocking seat for --class veto
                               (default MAGATAMA-3)
  --seat-order MODE            how the level's model pool maps onto
                               seats: convene (default; shuffled once
                               per convening), fixed (pool order, or
                               raati.seat_map), turn (reshuffled per
                               voting round — the final round respawns
                               every seat cold: no cross-round cache
                               reuse, evidence re-read per seat, for
                               the least fixed model-to-prior bias)
  --profile NAME               convene under a named profile: it fills
                               class/level/seats and the deliberation
                               shape for anything not given explicitly
                               here (explicit flags win; a profile's
                               pinned seats replace raati.level2).
                               Built-ins: triage, counsel, code-review,
                               ethics — override or extend via the user
                               config's raati.profiles

Shared flags like --provider/--model/--cwd work as usual; the whole
panel deliberates on that binding, e.g.:
  terva raati --provider ollama --model qwen3:8b "pizza for dinner?"
`))
}
