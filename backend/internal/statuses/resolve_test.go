package statuses

import (
	"slices"
	"testing"

	"github.com/flowboard/backend/internal/hierarchy"
)

func s(v string) *string   { return &v }
func f(v float64) *float64 { return &v }

func chain(ids ...string) []*hierarchy.Container {
	types := []string{"workspace", "space", "folder", "list"}
	var out []*hierarchy.Container
	for i, id := range ids {
		out = append(out, &hierarchy.Container{ID: id, Type: types[i]})
	}
	return out
}

func keys(set []Status) []string {
	var out []string
	for _, x := range set {
		out = append(out, x.Key)
	}
	return out
}

var base = map[string][]Def{
	"ws": {
		{ID: "1", Key: "todo", Name: s("To Do"), Category: s("not_started"), Color: s("#000000"), Position: f(1)},
		{ID: "2", Key: "doing", Name: s("Doing"), Category: s("active"), Color: s("#000000"), Position: f(2)},
		{ID: "3", Key: "done", Name: s("Done"), Category: s("done"), Color: s("#000000"), Position: f(3)},
	},
}

func with(extra map[string][]Def) map[string][]Def {
	out := map[string][]Def{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = append(out[k], v...)
	}
	return out
}

func TestResolve_InheritsWorkspaceDefaults(t *testing.T) {
	got := Resolve(chain("ws", "sp", "fo", "li"), base)
	if !slices.Equal(keys(got), []string{"todo", "doing", "done"}) {
		t.Fatal(keys(got))
	}
	if !got[0].Inherited || got[0].OwnerType != "workspace" {
		t.Fatalf("%+v", got[0])
	}
	if len(MissingCategories(got)) != 0 {
		t.Fatal("valid set reported missing categories")
	}
}

func TestResolve_OverrideAddHideAtDifferentLevels(t *testing.T) {
	defs := with(map[string][]Def{
		"sp": {{Key: "review", ID: "r", Name: s("Review"), Category: s("active"), Color: s("#111111"), Position: f(2.5)}},
		"fo": {{Key: "todo", Name: s("Backlog"), Position: f(10)}}, // rename + move to the end
		"li": {{Key: "review", Hidden: true}, {Key: "qa", ID: "q", Name: s("QA"), Category: s("active"), Color: s("#222222"), Position: f(2.7)}},
	})
	got := Resolve(chain("ws", "sp", "fo", "li"), defs)
	if !slices.Equal(keys(got), []string{"doing", "qa", "done", "todo"}) {
		t.Fatal(keys(got))
	}
	todo := Find(got, "todo")
	if todo.Name != "Backlog" || todo.Category != "not_started" || *todo.OverriddenAt != "fo" || todo.OwnerID != "ws" {
		t.Fatalf("%+v", todo)
	}
	// The sibling list without local config still sees "review".
	sib := Resolve(chain("ws", "sp", "fo", "li2"), defs)
	if Find(sib, "review") == nil || Find(sib, "qa") != nil {
		t.Fatal(keys(sib))
	}
}

func TestResolve_HiddenKeyCanBeReintroducedDeeper(t *testing.T) {
	defs := with(map[string][]Def{
		"sp": {{Key: "doing", Hidden: true}},
		"li": {{Key: "doing", ID: "d2", Name: s("Working"), Category: s("active"), Color: s("#333333"), Position: f(2)}},
	})
	if Find(Resolve(chain("ws", "sp"), defs), "doing") != nil {
		t.Fatal("hidden at space")
	}
	got := Find(Resolve(chain("ws", "sp", "fo", "li"), defs), "doing")
	if got == nil || got.Name != "Working" || got.OwnerID != "li" {
		t.Fatalf("%+v", got)
	}
}

func TestResolve_OrphanedOverrideIsIgnored(t *testing.T) {
	defs := with(map[string][]Def{"li": {{Key: "ghost", Name: s("Ghost")}}}) // no category/color, no base
	if Find(Resolve(chain("ws", "sp", "fo", "li"), defs), "ghost") != nil {
		t.Fatal("incomplete row for an unknown key must not create a column")
	}
}

func TestMissingCategoriesAndDefaultFor(t *testing.T) {
	defs := with(map[string][]Def{"li": {{Key: "done", Hidden: true}}})
	got := Resolve(chain("ws", "sp", "fo", "li"), defs)
	if !slices.Equal(MissingCategories(got), []string{"done"}) {
		t.Fatal(MissingCategories(got))
	}
	full := Resolve(chain("ws"), base)
	if DefaultFor(full, "active").Key != "doing" || DefaultFor(full, "closed").Key != "todo" {
		t.Fatal("DefaultFor should prefer same category, then not_started")
	}
}
