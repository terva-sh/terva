package talkoot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const savedRoster = `---
name: crew
title: Crew
home: /src/app
budget_usd_per_day: 7
team_budget_waived: true
color: "#336699"
members:
  - id: helm
    role: coordinator
    persona: mieli
  - id: jev
    role: specialist
    persona: kirjuri
---

Keep it short.
`

func TestARosterBecomesATemplateWithoutItsTeamKeys(t *testing.T) {
	isolatedHome(t)
	text, err := TemplateFromRoster([]byte(savedRoster), "Two members who keep it short.")
	if err != nil {
		t.Fatal(err)
	}
	s := string(text)
	for _, gone := range []string{"home:", "team_budget_waived", "color:"} {
		if strings.Contains(s, gone) {
			t.Errorf("the template keeps %s:\n%s", gone, s)
		}
	}
	if !strings.Contains(s, "description: Two members who keep it short.") || !strings.Contains(s, "Keep it short.") {
		t.Fatalf("the template lost its description or charter:\n%s", s)
	}

	path, err := SaveTemplate("my-crew", text)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != TemplatesDir() {
		t.Errorf("saved to %s, want the user tier %s", path, TemplatesDir())
	}
	tmpl, ok := LookupTemplate("", "my-crew")
	if !ok || tmpl.Source != SourceUser || tmpl.Problem != "" || tmpl.Members != 2 || tmpl.Description == "" {
		t.Fatalf("the saved template lists as %+v", tmpl)
	}
	// It instantiates into a roster again, in another checkout.
	again, err := Instantiate(tmpl, "/src/other", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(again, "my-crew.md")
	if err != nil {
		t.Fatal(err)
	}
	if r.Home != "/src/other" || r.BudgetUSDPerDay != 7 || r.TeamBudgetWaived || r.Color != "" {
		t.Errorf("the roster from the saved template = %+v", r)
	}
}

func TestSaveTemplateRefuses(t *testing.T) {
	isolatedHome(t)
	good, err := TemplateFromRoster([]byte(savedRoster), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveTemplate("crew", good); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveTemplate("crew", good); !errors.Is(err, ErrTemplateExists) {
		t.Errorf("a second save of one name: %v", err)
	}
	for _, name := range []string{"", "Crew", "repo:crew", "../crew", "a/b"} {
		if _, err := SaveTemplate(name, good); !errors.Is(err, ErrTemplateInvalid) {
			t.Errorf("SaveTemplate(%q) = %v, want ErrTemplateInvalid", name, err)
		}
	}
	if _, err := SaveTemplate("homed", []byte(savedRoster)); !errors.Is(err, ErrTemplateInvalid) {
		t.Errorf("a template that sets home: %v", err)
	}
	if _, err := SaveTemplate("big", append(good, make([]byte, maxTemplateSize)...)); !errors.Is(err, ErrTemplateInvalid) {
		t.Errorf("an oversized template: %v", err)
	}
	entries, err := os.ReadDir(TemplatesDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the user tier holds %d files, want only crew.md", len(entries))
	}
}
