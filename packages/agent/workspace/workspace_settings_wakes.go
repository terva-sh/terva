package workspace

import (
	"slices"
	"strconv"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

// talkootWakesOff is the value of the wakes row that removes the key.
const talkootWakesOff = "off"

// talkootWakeCaps are the caps that the wakes row offers. A cap that a
// person wrote into the configuration by hand joins them while it is set.
var talkootWakeCaps = []int{1, 2, 3, 5}

// talkootWakesItem is the settings row for talkoot_wakes: off, or the wakes
// that one member may have open at a time.
//
// 🔑 The row writes the user layer and nowhere else (MutateConfig), for the
// reason the Talkoot row does: a wake starts a turn with no new post from a
// person, and a cloned repository must not turn that on.
func talkootWakesItem(cfg config.Config) ctrlproto.SettingItem {
	caps := slices.Clone(talkootWakeCaps)
	value := talkootWakesOff
	if w := cfg.TalkootWakes; w != nil && w.PerMember >= 1 {
		value = strconv.Itoa(w.PerMember)
		if !slices.Contains(caps, w.PerMember) {
			caps = append(caps, w.PerMember)
			slices.Sort(caps)
		}
	}
	opts := []ctrlproto.SettingOption{{Value: talkootWakesOff, Label: i18n.T("Off")}}
	for _, n := range caps {
		opts = append(opts, ctrlproto.SettingOption{Value: strconv.Itoa(n), Label: i18n.T("%d for each member", n)})
	}
	return ctrlproto.SettingItem{
		Key: "talkoot_wakes", Group: "agents", Label: i18n.T("Member wakes"), Type: "enum",
		Value: value, Options: opts,
		Description: i18n.T("Let a member of a team set one later turn for itself, inside work that you started. The number is how many wakes one member may have open at a time. Off removes the talkoot_wakes key, with its longest delay."),
		Note:        i18n.T("applies at each member's next turn"),
	}
}

// setTalkootWakes writes the wakes row's value to the user layer. A cap keeps
// the max_delay that the key holds, and off removes the key.
func setTalkootWakes(val string) error {
	if val == talkootWakesOff {
		if err := config.MutateConfig(func(c *config.Config) { c.TalkootWakes = nil }); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
		return nil
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("member wakes must be off, or 1 or more"))
	}
	// 🚨 Only a cap that the row offers, or the one that the key holds now. A
	// client that sends a number of its own must not set a cap of thousands.
	//
	// 🔑 The test is inside the write, against the document that the write
	// changes. That document is the one file that the wake policy reads
	// (config.TalkootWakes), so a cap that passes as held is in the user
	// layer already, and no other layer can supply one.
	refused := false
	if err := config.MutateConfig(func(c *config.Config) {
		held := c.TalkootWakes != nil && c.TalkootWakes.PerMember == n
		if refused = !held && !slices.Contains(talkootWakeCaps, n); refused {
			return
		}
		if c.TalkootWakes == nil {
			c.TalkootWakes = &config.TalkootWakesConfig{}
		}
		c.TalkootWakes.PerMember = n
	}); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
	}
	if refused {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("member wakes must be off, or 1 or more"))
	}
	return nil
}
