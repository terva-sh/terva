package talkoot

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/look"
	"terva.sh/terva/packages/core/permission"
)

// A proposal is a batch of these operations on a roster's members (decision
// 0025). A member proposes, and only a person's approval applies the batch.
const (
	// OpAdd adds Member with the fields in Set.
	OpAdd = "add"
	// OpEdit sets the fields in Set on Member. A null or empty value removes
	// the field, so the member takes its default.
	OpEdit = "edit"
	// OpRemove removes Member. Its room history stays.
	OpRemove = "remove"
	// OpLook is an edit of look fields only.
	OpLook = "look"
	// OpTeam sets the team's own fields in Set (TeamFields). It names no
	// member. Only the team manager and a person propose one (decision 0025,
	// amendment of 2026-10-15).
	OpTeam = "team"
)

// MaxCharterBytes bounds a charter that a team op sets. A built-in template's
// charter runs near 1 KiB.
const MaxCharterBytes = 16 * 1024

// TeamFields gives each team field a proposal may change its class, in the
// order a card lists them. The title and the colour are look, the charter is
// voice, and the budget is authority (decision 0025, amendment).
//
// 🔑 team_budget_waived is absent on purpose. Only a person's talkoot.update
// waives the team cap (decision 0027).
var TeamFields = []struct {
	Name  string
	Class FieldClass
}{
	{"title", ClassLook},
	{"color", ClassLook},
	{"charter", ClassVoice},
	{"budget_usd_per_day", ClassAuthority},
}

// TeamClassOf returns the class of a team field a proposal may change, and
// false for any other name.
func TeamClassOf(field string) (FieldClass, bool) {
	for _, f := range TeamFields {
		if f.Name == field {
			return f.Class, true
		}
	}
	return "", false
}

// TeamChange is one team field before and after a change. A nil value is a
// field the roster does not set.
type TeamChange struct {
	Field  string `json:"field"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// MaxOps bounds one batch, so a card stays readable.
const MaxOps = 32

// MaxValueBytes bounds one string value. A roster line carries each changed
// member twice, and a card shows every value.
const MaxValueBytes = 256

// Op is one change to the members of a roster. Member names the member in
// every operation, the new one in an add, so Set never carries an id.
type Op struct {
	Op     string         `json:"op"`
	Member string         `json:"member"`
	Set    map[string]any `json:"set,omitempty"`
}

// MemberChange is one member before and after a change. Before is nil for a
// member the change added, and After is nil for one it removed.
type MemberChange struct {
	Member string  `json:"member"`
	Before *Member `json:"before,omitempty"`
	After  *Member `json:"after,omitempty"`
	// MarkBefore and MarkAfter are the member's whole marks, with the
	// defaults filled in, as each roster resolves them. A reset leaves no
	// mark in After, so a card draws these instead.
	MarkBefore *look.Mark `json:"mark_before,omitempty"`
	MarkAfter  *look.Mark `json:"mark_after,omitempty"`
}

// CheckOps checks the shape of a batch against the roster it would change:
// each operation, each field, and each member it names. It does not check the
// roster that results. ApplyOps and Validate do that.
func CheckOps(r Roster, ops []Op) error {
	if len(ops) == 0 {
		return errors.New("talkoot: a proposal needs at least one operation")
	}
	if len(ops) > MaxOps {
		return fmt.Errorf("talkoot: a proposal holds at most %d operations, and this one has %d", MaxOps, len(ops))
	}
	live := map[string]bool{}
	for _, m := range r.Members {
		live[m.ID] = true
	}
	for i, op := range ops {
		at := fmt.Sprintf("operation %d (%s %s)", i+1, op.Op, op.Member)
		if op.Op == OpTeam {
			if err := checkTeamOp(r, op); err != nil {
				return fmt.Errorf("talkoot: operation %d (team): %w", i+1, err)
			}
			continue
		}
		if !idPattern.MatchString(op.Member) {
			return fmt.Errorf("talkoot: %s: member id %q must be lower case letters, digits, and dashes, starting with a letter", at, op.Member)
		}
		for k, v := range op.Set {
			class, ok := ClassOf(k)
			switch {
			case !ok:
				return fmt.Errorf("talkoot: %s: %q is not a member field", at, k)
			case k == "id":
				return fmt.Errorf("talkoot: %s: an id cannot change; remove the member and add one", at)
			case op.Op == OpLook && class != ClassLook:
				return fmt.Errorf("talkoot: %s: %s is a %s field, and a look operation changes look fields only", at, k, class)
			}
			if err := checkValue(k, v); err != nil {
				return fmt.Errorf("talkoot: %s: %w", at, err)
			}
		}
		switch op.Op {
		case OpAdd:
			if live[op.Member] {
				return fmt.Errorf("talkoot: %s: %s is already a member", at, op.Member)
			}
			live[op.Member] = true
		case OpEdit, OpLook:
			if !live[op.Member] {
				return fmt.Errorf("talkoot: %s: %s is not a member", at, op.Member)
			}
			if len(op.Set) == 0 {
				return fmt.Errorf("talkoot: %s: set names no field to change", at)
			}
		case OpRemove:
			if !live[op.Member] {
				return fmt.Errorf("talkoot: %s: %s is not a member", at, op.Member)
			}
			if len(op.Set) > 0 {
				return fmt.Errorf("talkoot: %s: a remove takes no fields", at)
			}
			delete(live, op.Member)
		default:
			return fmt.Errorf("talkoot: %s: the operation must be add, edit, remove, look, or team", at)
		}
	}
	return nil
}

// checkTeamOp checks one team op: no member, at least one field, each field a
// team field, and each value of its kind.
//
// 🔑 The field's current value must fit a proposal too. Parse sets no size
// limit, so talkoot.update can write a charter above MaxCharterBytes. A
// proposal that shortened it would apply, and its undo, which restores the
// old value through the same checks, would be refused. So every approved team
// change keeps an undo, and a person changes such a field with talkoot.update.
func checkTeamOp(r Roster, op Op) error {
	now := teamValues(r)
	if op.Member != "" {
		return fmt.Errorf("a team operation names no member, and this one names %q", op.Member)
	}
	if len(op.Set) == 0 {
		return errors.New("set names no team field to change")
	}
	for k, v := range op.Set {
		if k == "team_budget_waived" {
			return errors.New("no proposal waives the team budget; a person does that with talkoot.update (decision 0027)")
		}
		if _, ok := TeamClassOf(k); !ok {
			return fmt.Errorf("%q is not a team field a proposal may change; it changes title, color, charter, or budget_usd_per_day", k)
		}
		if err := checkTeamValue(k, v); err != nil {
			return err
		}
		if err := checkTeamValue(k, now[k]); err != nil {
			return fmt.Errorf("the team's %s now does not fit a proposal (%v), so the undo of this change could not restore it; a person changes it with talkoot.update", k, err)
		}
	}
	return nil
}

func checkTeamValue(field string, v any) error {
	if field == "budget_usd_per_day" {
		var n float64
		switch x := v.(type) {
		case int:
			n = float64(x)
		case float64:
			n = x
		default:
			return errors.New("budget_usd_per_day must be a number; a team always keeps a daily budget")
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
			return errors.New("budget_usd_per_day must be a positive number")
		}
		return nil
	}
	if unset(v) {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", field)
	}
	limit := MaxValueBytes
	if field == "charter" {
		limit = MaxCharterBytes
	}
	if len(s) > limit {
		return fmt.Errorf("%s is %d bytes, above the %d limit", field, len(s), limit)
	}
	if field == "color" && !look.ValidColor(s) {
		return fmt.Errorf("color %q is not a #RRGGBB value", s)
	}
	if field == "title" && strings.ContainsAny(s, "\r\n") {
		return errors.New("a title is one line")
	}
	return nil
}

// HasTeamOp reports whether a batch changes a team field.
func HasTeamOp(ops []Op) bool {
	return slices.ContainsFunc(ops, func(op Op) bool { return op.Op == OpTeam })
}

// applyTeam sets a team op's fields on a parsed roster, as the text would
// parse after the op.
func applyTeam(r *Roster, op Op) {
	for k, v := range op.Set {
		s, _ := v.(string)
		switch k {
		case "title":
			r.Title = s
		case "color":
			r.Color = s
		case "charter":
			r.Charter = strings.TrimSpace(s)
		case "budget_usd_per_day":
			switch n := v.(type) {
			case int:
				r.BudgetUSDPerDay = float64(n)
			case float64:
				r.BudgetUSDPerDay = n
			}
		}
	}
}

// teamValues returns the team fields a proposal may change, by name. An unset
// string is nil.
func teamValues(r Roster) map[string]any {
	str := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	return map[string]any{
		"title": str(r.Title), "color": str(r.Color), "charter": str(r.Charter),
		"budget_usd_per_day": r.BudgetUSDPerDay,
	}
}

// DiffTeam returns each team field that differs between two rosters, in the
// order of TeamFields.
func DiffTeam(before, after Roster) []TeamChange {
	was, is := teamValues(before), teamValues(after)
	var out []TeamChange
	for _, f := range TeamFields {
		if !reflect.DeepEqual(was[f.Name], is[f.Name]) {
			out = append(out, TeamChange{Field: f.Name, Before: was[f.Name], After: is[f.Name]})
		}
	}
	return out
}

// TeamWidens reports whether a team change grants more: a daily budget
// raised.
func TeamWidens(c TeamChange) bool {
	if c.Field != "budget_usd_per_day" {
		return false
	}
	was, ok1 := c.Before.(float64)
	is, ok2 := c.After.(float64)
	return ok1 && ok2 && is > was
}

// fieldKinds maps each member field's YAML name to its Go kind.
var fieldKinds = func() map[string]reflect.Kind {
	out := map[string]reflect.Kind{}
	rt := reflect.TypeFor[Member]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("yaml"), ",")
		out[name] = rt.Field(i).Type.Kind()
	}
	return out
}()

// checkValue checks a value against its field's type. A null or an empty
// string removes the field, whatever its type.
//
// 🚨 The YAML decoder puts 2.5 into an int as 2. So a whole number is checked
// here, or a proposal would apply a cap other than the one on its card.
func checkValue(field string, v any) error {
	if unset(v) {
		return nil
	}
	var ok bool
	switch fieldKinds[field] {
	case reflect.String:
		var str string
		str, ok = v.(string)
		if ok && len(str) > MaxValueBytes {
			return fmt.Errorf("%s is %d bytes, above the %d limit", field, len(str), MaxValueBytes)
		}
	case reflect.Bool:
		_, ok = v.(bool)
	case reflect.Int:
		switch n := v.(type) {
		case int:
			ok = true
		case float64:
			ok = n == math.Trunc(n) && math.Abs(n) < 1<<31
		}
	case reflect.Float64:
		switch n := v.(type) {
		case int:
			ok = true
		case float64:
			ok = !math.IsNaN(n) && !math.IsInf(n, 0)
		}
	case reflect.Slice:
		// A list of strings. It arrives as []any from JSON, and as []string
		// from Go.
		var items []any
		switch l := v.(type) {
		case []any:
			items, ok = l, true
		case []string:
			for _, s := range l {
				items = append(items, s)
			}
			ok = true
		}
		for _, item := range items {
			s, isString := item.(string)
			if !isString {
				ok = false
				break
			}
			if len(s) > MaxValueBytes {
				return fmt.Errorf("%s holds an entry of %d bytes, above the %d limit", field, len(s), MaxValueBytes)
			}
		}
	case reflect.Pointer:
		// A mark: shape and color. It arrives as a map from JSON, and as a
		// look.Mark from Go.
		var m look.Mark
		switch x := v.(type) {
		case look.Mark:
			m, ok = x, true
		case *look.Mark:
			ok = x != nil
			if ok {
				m = *x
			}
		case map[string]any:
			ok = true
			for k, val := range x {
				s, isString := val.(string)
				switch {
				case !isString:
					ok = false
				case k == "shape":
					m.Shape = s
				case k == "color":
					m.Color = s
				default:
					return fmt.Errorf("%s has no field %q; it takes shape and color", field, k)
				}
			}
		}
		if ok {
			if p := look.Check(m); len(p) > 0 {
				return fmt.Errorf("%s", strings.Join(p, "; "))
			}
		}
	}
	if !ok {
		want := map[reflect.Kind]string{reflect.String: "a string", reflect.Bool: "true or false", reflect.Int: "a whole number", reflect.Float64: "a number", reflect.Slice: "a list of strings", reflect.Pointer: "a mark with a shape and a color"}[fieldKinds[field]]
		return fmt.Errorf("%s must be %s", field, want)
	}
	return nil
}

// ApplyOps applies a checked batch to the text of talkoot.md. It edits the
// frontmatter as a YAML tree, so the person's comments and field order
// survive, and it keeps the charter as it was.
//
// 🔑 The tree edit is checked against a second path. The same batch is applied
// to the fields each member has in the text, and the members the new text
// parses to must equal them. A tree edit that meant something else, such as a
// key set twice, then fails here and never reaches a card.
//
// ⚠️ The second path starts from the fields as written, not from the parsed
// members. A default can depend on another field (a member's posture follows
// its workspace), and a parsed member has its defaults written in.
func ApplyOps(text []byte, ops []Op) ([]byte, error) {
	before, err := Parse(text, FileName)
	if err != nil {
		return nil, err
	}
	if err := CheckOps(before, ops); err != nil {
		return nil, err
	}
	front, body, ok := splitFrontmatter(text)
	if !ok {
		return nil, errors.New("talkoot: the roster has no YAML frontmatter")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("talkoot: the roster frontmatter is not a mapping")
	}
	members := valueOf(doc.Content[0], "members")
	if members == nil || members.Kind != yaml.SequenceNode {
		return nil, errors.New("talkoot: the roster has no members list")
	}
	for _, op := range ops {
		if op.Op == OpTeam {
			if err := applyTeamNode(doc.Content[0], op); err != nil {
				return nil, err
			}
			if c, ok := op.Set["charter"]; ok {
				s, _ := c.(string)
				if s = strings.TrimSpace(s); s == "" {
					body = nil
				} else {
					body = []byte("\n" + s + "\n")
				}
			}
			continue
		}
		if err := applyNode(members, op); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	out := append([]byte("---\n"), buf.Bytes()...)
	out = append(out, "---\n"...)
	out = append(out, body...)

	after, err := Parse(out, FileName)
	if err != nil {
		return nil, err
	}
	raw, err := rawMembers(front)
	if err != nil {
		return nil, err
	}
	want, err := decodeMembers(applyRaw(raw, ops))
	if err != nil {
		return nil, err
	}
	before.Members = want
	for _, op := range ops {
		if op.Op == OpTeam {
			applyTeam(&before, op)
		}
	}
	if !reflect.DeepEqual(before, after) {
		return nil, errors.New("talkoot: the roster text did not take the change as proposed")
	}
	return out, nil
}

// applyTeamNode sets a team op's frontmatter fields on the roster's top
// mapping. The charter is the body, so ApplyOps writes it.
func applyTeamNode(root *yaml.Node, op Op) error {
	for _, f := range TeamFields {
		v, ok := op.Set[f.Name]
		if !ok || f.Name == "charter" {
			continue
		}
		if unset(v) {
			dropField(root, f.Name)
			continue
		}
		if err := setField(root, f.Name, v); err != nil {
			return err
		}
	}
	return nil
}

// valueOf returns the value node of key in a mapping node.
func valueOf(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// memberNode returns the index in the members list of the member with id.
func memberNode(members *yaml.Node, id string) int {
	for i, item := range members.Content {
		if item.Kind == yaml.MappingNode {
			if v := valueOf(item, "id"); v != nil && v.Value == id {
				return i
			}
		}
	}
	return -1
}

func applyNode(members *yaml.Node, op Op) error {
	switch op.Op {
	case OpAdd:
		item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if err := setField(item, "id", op.Member); err != nil {
			return err
		}
		for _, f := range MemberFields {
			if v, ok := op.Set[f.Name]; ok && !unset(v) {
				if err := setField(item, f.Name, v); err != nil {
					return err
				}
			}
		}
		members.Content = append(members.Content, item)
	case OpEdit, OpLook:
		i := memberNode(members, op.Member)
		if i < 0 {
			return fmt.Errorf("talkoot: %s is not a member", op.Member)
		}
		for _, f := range MemberFields {
			v, ok := op.Set[f.Name]
			if !ok {
				continue
			}
			if unset(v) {
				dropField(members.Content[i], f.Name)
				continue
			}
			if err := setField(members.Content[i], f.Name, v); err != nil {
				return err
			}
		}
	case OpRemove:
		i := memberNode(members, op.Member)
		if i < 0 {
			return fmt.Errorf("talkoot: %s is not a member", op.Member)
		}
		members.Content = slices.Delete(members.Content, i, i+1)
	}
	return nil
}

// unset reports whether a value in Set removes its field.
func unset(v any) bool { return v == nil || v == "" }

// setField sets key in a mapping node. A value that replaces another keeps
// the comments on the one it replaces, and a list keeps the flow style the
// person wrote, such as tools: [read].
func setField(m *yaml.Node, key string, v any) error {
	var val yaml.Node
	if err := val.Encode(v); err != nil {
		return fmt.Errorf("talkoot: %s: %w", key, err)
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			old := m.Content[i+1]
			val.HeadComment, val.LineComment, val.FootComment = old.HeadComment, old.LineComment, old.FootComment
			if old.Kind == yaml.SequenceNode && val.Kind == yaml.SequenceNode {
				val.Style |= old.Style & yaml.FlowStyle
			}
			m.Content[i+1] = &val
			return nil
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &val)
	return nil
}

func dropField(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}

// rawMembers returns each member's fields as the roster text writes them,
// before any default applies.
func rawMembers(front []byte) ([]map[string]any, error) {
	var raw struct {
		Members []map[string]any `yaml:"members"`
	}
	if err := yaml.Unmarshal(front, &raw); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	return raw.Members, nil
}

// applyRaw applies a checked batch to members' written fields.
func applyRaw(in []map[string]any, ops []Op) []map[string]any {
	out := make([]map[string]any, 0, len(in)+len(ops))
	for _, m := range in {
		out = append(out, maps.Clone(m))
	}
	for _, op := range ops {
		i := slices.IndexFunc(out, func(m map[string]any) bool { return m["id"] == op.Member })
		switch op.Op {
		case OpAdd:
			out = append(out, map[string]any{"id": op.Member})
			i = len(out) - 1
			fallthrough
		case OpEdit, OpLook:
			if i < 0 {
				continue
			}
			for k, v := range op.Set {
				if unset(v) {
					delete(out[i], k)
				} else {
					out[i][k] = v
				}
			}
		case OpRemove:
			if i >= 0 {
				out = slices.Delete(out, i, i+1)
			}
		case OpTeam:
			// A team op changes no member.
		}
	}
	return out
}

// decodeMembers reads members from their written fields, with the defaults
// Parse applies.
func decodeMembers(in []map[string]any) ([]Member, error) {
	out := make([]Member, 0, len(in))
	for _, fields := range in {
		raw, err := yaml.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("talkoot: %w", err)
		}
		var m Member
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("talkoot: member %v: %w", fields["id"], err)
		}
		applyDefaults(&m)
		out = append(out, m)
	}
	return out, nil
}

// fieldsOf returns a member's set fields by their YAML names, without its id.
func fieldsOf(m Member) (map[string]any, error) {
	raw, err := yaml.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	fields := map[string]any{}
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	delete(fields, "id")
	return fields, nil
}

// explicitFields is fieldsOf without a native driver and a shared workspace,
// the defaults that depend on nothing else, so an undo does not write them
// into a roster that never had them.
//
// 🔑 The posture always stays. Its default follows the workspace, and a
// parsed member cannot say whether the person wrote it. An undo that dropped
// a written posture: plan would let a later move to a worktree raise the
// member to auto-edit. Kept, a posture can only stay as narrow as it was.
func explicitFields(m Member) (map[string]any, error) {
	fields, err := fieldsOf(m)
	if err != nil {
		return nil, err
	}
	if m.Driver == DriverNative {
		delete(fields, "driver")
	}
	if m.Workspace == WorkspaceShared {
		delete(fields, "workspace")
	}
	return fields, nil
}

// Diff returns every member that differs between two rosters: the members of
// after in its order, then the members it removed in the order of before.
func Diff(before, after Roster) []MemberChange {
	var out []MemberChange
	for _, a := range after.Members {
		b, ok := before.member(a.ID)
		switch {
		case !ok:
			out = append(out, MemberChange{Member: a.ID, After: &a})
		case !reflect.DeepEqual(b, a):
			out = append(out, MemberChange{Member: a.ID, Before: &b, After: &a})
		}
	}
	for _, b := range before.Members {
		if _, ok := after.member(b.ID); !ok {
			out = append(out, MemberChange{Member: b.ID, Before: &b})
		}
	}
	return out
}

// Inverse returns the batch that undoes changes: it removes an added member,
// adds back a removed one with its old fields, and sets each edited field
// back. A driver or a workspace that held its default goes back to unset
// rather than to its value, and a posture is always written. A member added
// back joins at the end of the list. Each team field goes back too, in one
// team op.
func Inverse(changes []MemberChange, team []TeamChange) ([]Op, error) {
	var ops []Op
	if len(team) > 0 {
		set := map[string]any{}
		for _, c := range team {
			set[c.Field] = c.Before
		}
		ops = append(ops, Op{Op: OpTeam, Set: set})
	}
	for _, c := range changes {
		switch {
		case c.Before == nil && c.After != nil:
			ops = append(ops, Op{Op: OpRemove, Member: c.Member})
		case c.Before != nil && c.After == nil:
			fields, err := explicitFields(*c.Before)
			if err != nil {
				return nil, err
			}
			ops = append(ops, Op{Op: OpAdd, Member: c.Member, Set: fields})
		case c.Before != nil && c.After != nil:
			was, err := explicitFields(*c.Before)
			if err != nil {
				return nil, err
			}
			is, err := explicitFields(*c.After)
			if err != nil {
				return nil, err
			}
			set := map[string]any{}
			for k, v := range was {
				if !reflect.DeepEqual(is[k], v) {
					set[k] = v
				}
			}
			for k := range is {
				if _, ok := was[k]; !ok {
					set[k] = nil
				}
			}
			if len(set) > 0 {
				ops = append(ops, Op{Op: OpEdit, Member: c.Member, Set: set})
			}
		}
	}
	if len(ops) == 0 {
		return nil, errors.New("talkoot: the change has nothing to undo")
	}
	return ops, nil
}

// postureRank orders the approval modes by what each lets a member do alone.
var postureRank = map[string]int{
	string(permission.ApprovalPlan):      0,
	string(permission.ApprovalAsk):       1,
	string(permission.ApprovalAutoEdit):  2,
	string(permission.ApprovalWorkspace): 3,
	string(permission.ApprovalYolo):      4,
}

// baseline is what a change is measured against: the member before it, or a
// default member for an add.
func baseline(c MemberChange) Member {
	if c.Before != nil {
		return *c.Before
	}
	was := Member{ID: c.Member, Role: RoleSpecialist}
	applyDefaults(&was)
	return was
}

// Widens names the authority fields a change grants more through:
//   - a posture that ranks higher, or one the ranking does not know;
//   - a spend or turn cap raised or lifted;
//   - the coordinator role, or the reviewer flag;
//   - a worker driver it did not have;
//   - a move of a writing member into the shared checkout;
//   - a tools list lifted, or one that gains a tool the old list did not
//     allow.
//
// An added member is measured against a default member. Every other change
// to an authority field is in AuthorityChanges.
func Widens(c MemberChange) []string {
	if c.After == nil {
		return nil
	}
	was, is := baseline(c), *c.After
	var out []string
	if rank, known := postureRank[is.Posture]; !known || rank > postureRank[was.Posture] {
		out = append(out, "posture")
	}
	if capWidens(was.BudgetUSDPerDay, is.BudgetUSDPerDay) {
		out = append(out, "budget_usd_per_day")
	}
	if capWidens(float64(was.TurnsPerDay), float64(is.TurnsPerDay)) {
		out = append(out, "turns_per_day")
	}
	if is.Role == RoleCoordinator && was.Role != RoleCoordinator {
		out = append(out, "role")
	}
	if is.Reviewer && !was.Reviewer {
		out = append(out, "reviewer")
	}
	if is.Driver != was.Driver && is.Driver != DriverNative {
		out = append(out, "driver")
	}
	if was.Workspace != WorkspaceShared && is.Workspace == WorkspaceShared && is.Writes() {
		out = append(out, "workspace")
	}
	if toolsWiden(was.Tools, is.Tools) {
		out = append(out, "tools")
	}
	return out
}

// AuthorityChanges names every authority field a change sets to another
// value, whether or not it widens. A card marks them all, so a change Widens
// does not rank, such as a model, still shows.
func AuthorityChanges(c MemberChange) []string {
	if c.After == nil {
		return nil
	}
	was, err := fieldsOf(baseline(c))
	if err != nil {
		return nil
	}
	is, err := fieldsOf(*c.After)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range MemberFields {
		if f.Class == ClassAuthority && f.Name != "id" && !reflect.DeepEqual(was[f.Name], is[f.Name]) {
			out = append(out, f.Name)
		}
	}
	return out
}

// capWidens reports whether a cap grows. Zero is no cap, so dropping a cap to
// zero lifts it.
func capWidens(was, is float64) bool {
	if was <= 0 || math.IsNaN(is) {
		return false
	}
	return is <= 0 || is > was
}

// SelfAuthority reports whether a member's batch changes its own authority:
// it removes the member, or sets one of its authority fields. The card says so
// in its title, because a member that widens itself is the case 0022 exists
// to keep from happening unseen.
func SelfAuthority(proposer string, ops []Op) bool {
	for _, op := range ops {
		if op.Member != proposer {
			continue
		}
		if op.Op == OpRemove || op.Op == OpAdd {
			return true
		}
		for k := range op.Set {
			if class, _ := ClassOf(k); class == ClassAuthority {
				return true
			}
		}
	}
	return false
}

// ClassOfOps returns the widest class a batch touches. An add and a remove
// are authority.
func ClassOfOps(ops []Op) FieldClass {
	rank := map[FieldClass]int{ClassLook: 0, ClassVoice: 1, ClassAuthority: 2}
	out := ClassLook
	for _, op := range ops {
		if op.Op == OpAdd || op.Op == OpRemove {
			return ClassAuthority
		}
		classOf := ClassOf
		if op.Op == OpTeam {
			classOf = TeamClassOf
		}
		for k := range op.Set {
			if class, _ := classOf(k); rank[class] > rank[out] {
				out = class
			}
		}
	}
	return out
}

// Summarize says in one line what a batch does, for the envelope that records
// it in the room.
func Summarize(ops []Op) string {
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		keys := make([]string, 0, len(op.Set))
		for k := range op.Set {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		s := op.Op + " " + op.Member
		if op.Op == OpTeam {
			s = "team"
		}
		if len(keys) > 0 && op.Op != OpAdd {
			s += " (" + strings.Join(keys, ", ") + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}
