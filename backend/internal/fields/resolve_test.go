package fields

import (
	"encoding/json"
	"testing"

	"github.com/flowboard/backend/internal/hierarchy"
)

var schema = []Def{
	{Key: "pts", Name: "Points", Type: "number"},
	{Key: "comp", Name: "Component", Type: "single_select", Required: true, Options: []Option{{ID: "fe", Label: "FE"}, {ID: "be", Label: "BE"}}},
	{Key: "tags", Name: "Tags", Type: "multi_select", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
	{Key: "due", Name: "Due", Type: "date"},
	{Key: "ok", Name: "OK", Type: "checkbox"},
	{Key: "note", Name: "Note", Type: "text"},
}

func raw(m map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range m {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}

func TestApply_TypeErrorsCarryFieldPaths(t *testing.T) {
	_, errs := Apply(schema, nil, raw(map[string]any{
		"pts": "3", "comp": 1, "tags": []string{"a", "z"}, "due": "tomorrow", "ok": "yes", "note": 5, "nope": 1,
	}), true)
	want := map[string]string{
		"customFields.pts": "INVALID_TYPE", "customFields.comp": "INVALID_TYPE", "customFields.tags[1]": "INVALID_OPTION",
		"customFields.due": "INVALID_FORMAT", "customFields.ok": "INVALID_TYPE", "customFields.note": "INVALID_TYPE",
		"customFields.nope": "UNKNOWN_FIELD",
	}
	got := map[string]string{}
	for _, e := range errs {
		got[e.Path] = e.Code
	}
	for p, c := range want {
		if got[p] != c {
			t.Errorf("%s: want %s got %q", p, c, got[p])
		}
	}
	// comp already reported as INVALID_TYPE, so no duplicate REQUIRED for it.
	n := 0
	for _, e := range errs {
		if e.Path == "customFields.comp" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("comp reported %d times", n)
	}
}

func TestApply_RequiredAndClearing(t *testing.T) {
	_, errs := Apply(schema, nil, raw(map[string]any{"pts": 1}), true)
	if len(errs) != 1 || errs[0].Path != "customFields.comp" || errs[0].Code != "REQUIRED" {
		t.Fatalf("%+v", errs)
	}
	merged, errs := Apply(schema, map[string]any{"comp": "fe", "note": "x"}, raw(map[string]any{"note": nil, "tags": []string{}}), true)
	if len(errs) != 0 || merged["note"] != nil || merged["tags"] != nil || merged["comp"] != "fe" {
		t.Fatalf("%v %v", merged, errs)
	}
	_, errs = Apply(schema, map[string]any{"comp": "fe"}, raw(map[string]any{"comp": nil}), true)
	if len(errs) != 1 || errs[0].Code != "REQUIRED" {
		t.Fatalf("clearing a required field must fail: %+v", errs)
	}
}

func TestRetainAndResolveOrder(t *testing.T) {
	kept, dropped := Retain(schema[:2], map[string]any{"pts": 1, "zzz": 2, "aaa": 3})
	if len(kept) != 1 || len(dropped) != 2 || dropped[0] != "aaa" {
		t.Fatalf("%v %v", kept, dropped)
	}
	c := []*hierarchy.Container{{ID: "sp", Type: "space"}, {ID: "li", Type: "list"}}
	got := Resolve(c, map[string][]Def{
		"li": {{Key: "l2", Position: 2}, {Key: "l1", Position: 1}},
		"sp": {{Key: "s1", Position: 5}},
	})
	if got[0].Key != "s1" || got[1].Key != "l1" || got[2].Key != "l2" || !got[0].Inherited || got[1].Inherited {
		t.Fatalf("%+v", got)
	}
}
