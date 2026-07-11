package build

// Build-layer wiring helpers for the RAATI deliberation primitive
// (docs/proposals/raati-deliberation.md). These bridge the config and
// raati packages, and are shared by the two front-ends that live in
// different packages now: the `terva raati` CLI (package agent) and the
// web deliberation board (package workspace). Keeping them here — the
// one place that already imports both config and raati — avoids a
// duplicated adapter or a config→raati dependency.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/raati"
)

// WriteRaatiRecord persists a full deliberation record (both rounds,
// tally, minority report, and the agent ids for transcript lookup)
// under <TervaHome>/raati/. raati.Result.MarshalRecord stays pure
// (bytes only); the filesystem and home-directory concern lives here.
func WriteRaatiRecord(res *raati.Result) (string, error) {
	dir := filepath.Join(config.TervaHome(), "raati")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	raw, err := res.MarshalRecord()
	if err != nil {
		return "", err
	}
	// The record id is the write-time UnixNano (history sorts on it),
	// but Windows' wall clock advances in ~0.5ms steps, so two
	// back-to-back convenings can observe the same nano. O_EXCL plus a
	// bump keeps the second record from silently overwriting the first
	// while preserving write order.
	nano := time.Now().UnixNano()
	for {
		path := filepath.Join(dir, fmt.Sprintf("raati-%d.json", nano))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			nano++
			continue
		}
		if err != nil {
			return "", err
		}
		if _, werr := f.Write(raw); werr != nil {
			f.Close()
			return "", werr
		}
		if cerr := f.Close(); cerr != nil {
			return "", cerr
		}
		return path, nil
	}
}

// RaatiLevel2Bindings maps the user config's raati.level2 seats onto the
// coordinator's binding type.
func RaatiLevel2Bindings(uc config.Config) []raati.Binding {
	out := make([]raati.Binding, 0, len(uc.Raati.Level2))
	for _, b := range uc.Raati.Level2 {
		out = append(out, raati.Binding{Provider: b.Provider, Model: b.Model})
	}
	return out
}

// RaatiProfiles assembles the convening profiles for the three
// surfaces: the shipped built-ins (unless raati.builtin_profiles is
// explicitly false) under the user config's raati.profiles — a user
// entry with a built-in's name replaces it wholesale. Config values
// pass through raw — validation happens where a profile is used, so a
// typo errors that convening with a message instead of silently
// dropping the profile here.
func RaatiProfiles(uc config.Config) map[string]raati.Profile {
	out := map[string]raati.Profile{}
	if uc.Raati.BuiltinProfiles == nil || *uc.Raati.BuiltinProfiles {
		out = raati.BuiltinProfiles()
	}
	for name, p := range uc.Raati.Profiles {
		seats := make([]raati.Binding, 0, len(p.Seats))
		for _, b := range p.Seats {
			seats = append(seats, raati.Binding{Provider: b.Provider, Model: b.Model})
		}
		prof := raati.Profile{
			Description: p.Description,
			Seats:       seats,
			SeatOrder:   p.SeatOrder,
			SeatMap:     append([]int(nil), p.SeatMap...),
			Class:       p.Class,
			Inquire:     p.Inquire,
		}
		if p.Level != nil {
			if p.Level.Auto {
				prof.AutoLevel = true
				prof.AutoCeiling = p.Level.Ceiling
			} else if p.Level.Value != nil {
				v := *p.Level.Value
				prof.Level = &v
			}
		}
		if p.SingleRound != nil {
			v := *p.SingleRound
			prof.SingleRound = &v
		}
		if p.Converge != nil {
			v := *p.Converge
			prof.Converge = &v
		}
		out[name] = prof
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LoadPersonaByName exposes the by-name persona lookup to callers outside
// build (the raati board colors panel blocks by persona accent). Unlike
// ResolvePersona, an empty name is NOT defaulted — the caller always
// passes a specific panelist name.
func LoadPersonaByName(name string) (Persona, error) {
	return loadPersonaByName(name)
}
