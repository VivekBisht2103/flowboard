// Package fields implements custom-field definitions scoped to the hierarchy and
// validation of task values against a list's resolved schema.
//
// Resolution: the schema for a list is the union of field definitions owned by
// its space, folder and the list itself, ordered by depth then position.
// Key collisions are rejected at definition time: a key may appear at most once
// along any root-to-leaf path (checked against ancestors AND descendants under a
// workspace config lock). So there is never an ambiguous "which definition wins".
//
// Values live in tasks.custom_fields as { "<key>": <json value> }:
//
//	text          -> string (<=5000 chars; "" clears)
//	number        -> JSON number
//	single_select -> option id (string)
//	multi_select  -> array of option ids (deduplicated; [] clears)
//	date          -> "YYYY-MM-DD" (RFC3339 accepted and truncated to its calendar date)
//	checkbox      -> boolean
package fields

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flowboard/backend/internal/hierarchy"
	"github.com/flowboard/backend/internal/platform/httpx"
)

var Types = []string{"text", "number", "single_select", "multi_select", "date", "checkbox"}

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Color string `json:"color,omitempty"`
}

type Def struct {
	ID        string   `json:"id"`
	OwnerID   string   `json:"ownerId"`
	OwnerType string   `json:"ownerType"`
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Required  bool     `json:"required"`
	Options   []Option `json:"options"`
	Position  float64  `json:"position"`
	Inherited bool     `json:"inherited"`
}

func IsSelect(t string) bool { return t == "single_select" || t == "multi_select" }

// Resolve returns the schema at the end of chain (pure).
func Resolve(chain []*hierarchy.Container, defs map[string][]Def) []Def {
	var out []Def
	seen := map[string]bool{}
	for depth, owner := range chain {
		local := slices.Clone(defs[owner.ID])
		sort.SliceStable(local, func(i, j int) bool { return local[i].Position < local[j].Position })
		for _, d := range local {
			if seen[d.Key] { // defensive: should be impossible given write-time checks
				continue
			}
			seen[d.Key] = true
			d.OwnerType = owner.Type
			d.Inherited = depth < len(chain)-1
			out = append(out, d)
		}
	}
	if out == nil {
		out = []Def{}
	}
	return out
}

func byKey(schema []Def) map[string]*Def {
	m := make(map[string]*Def, len(schema))
	for i := range schema {
		m[schema[i].Key] = &schema[i]
	}
	return m
}

// NormalizeValue validates a single value against a definition and returns the
// canonical value to store. A nil return with no error means "clear".
func NormalizeValue(d *Def, raw json.RawMessage, path string) (any, []httpx.FieldError) {
	fe := func(code, msg string) []httpx.FieldError {
		return []httpx.FieldError{{Path: path, Code: code, Message: msg}}
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	switch d.Type {
	case "text":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fe("INVALID_TYPE", "expected a string")
		}
		if len([]rune(s)) > 5000 {
			return nil, fe("TOO_LONG", "text must be at most 5000 characters")
		}
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		return s, nil
	case "number":
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fe("INVALID_TYPE", "expected a number")
		}
		return f, nil
	case "checkbox":
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fe("INVALID_TYPE", "expected a boolean")
		}
		return b, nil
	case "date":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fe("INVALID_TYPE", "expected a date string (YYYY-MM-DD)")
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t.Format("2006-01-02"), nil
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Format("2006-01-02"), nil
		}
		return nil, fe("INVALID_FORMAT", "expected YYYY-MM-DD or RFC3339")
	case "single_select":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fe("INVALID_TYPE", "expected an option id string")
		}
		if !hasOption(d, s) {
			return nil, fe("INVALID_OPTION", fmt.Sprintf("%q is not an option; valid: %s", s, optionIDs(d)))
		}
		return s, nil
	case "multi_select":
		var arr []string
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fe("INVALID_TYPE", "expected an array of option id strings")
		}
		var out []string
		var errs []httpx.FieldError
		for i, s := range arr {
			if !hasOption(d, s) {
				errs = append(errs, httpx.FieldError{Path: fmt.Sprintf("%s[%d]", path, i), Code: "INVALID_OPTION",
					Message: fmt.Sprintf("%q is not an option; valid: %s", s, optionIDs(d))})
				continue
			}
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		if len(errs) > 0 {
			return nil, errs
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
	return nil, fe("UNSUPPORTED_TYPE", "unsupported field type "+d.Type)
}

func hasOption(d *Def, id string) bool {
	for _, o := range d.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

func optionIDs(d *Def) string {
	ids := make([]string, len(d.Options))
	for i, o := range d.Options {
		ids[i] = o.ID
	}
	return strings.Join(ids, ", ")
}

// Apply merges a patch of raw values onto existing stored values and validates the
// result against schema. Unknown keys are rejected; null clears a value. When
// checkRequired is set, every required field must be present in the merged result.
func Apply(schema []Def, existing map[string]any, patch map[string]json.RawMessage, checkRequired bool) (map[string]any, []httpx.FieldError) {
	idx := byKey(schema)
	merged := map[string]any{}
	for k, v := range existing {
		merged[k] = v
	}
	var errs []httpx.FieldError
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := "customFields." + k
		d := idx[k]
		if d == nil {
			errs = append(errs, httpx.FieldError{Path: path, Code: "UNKNOWN_FIELD", Message: "no custom field with this key applies to the list"})
			continue
		}
		v, fe := NormalizeValue(d, patch[k], path)
		if len(fe) > 0 {
			errs = append(errs, fe...)
			continue
		}
		if v == nil {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	if checkRequired {
		for _, d := range schema {
			if !d.Required {
				continue
			}
			if _, ok := merged[d.Key]; !ok {
				if _, reported := patch[d.Key]; reported && hasErrFor(errs, "customFields."+d.Key) {
					continue
				}
				errs = append(errs, httpx.FieldError{Path: "customFields." + d.Key, Code: "REQUIRED", Message: d.Name + " is required"})
			}
		}
	}
	return merged, errs
}

func hasErrFor(errs []httpx.FieldError, path string) bool {
	for _, e := range errs {
		if e.Path == path || strings.HasPrefix(e.Path, path+"[") {
			return true
		}
	}
	return false
}

// Retain keeps only values whose key exists in schema; it returns the dropped keys.
func Retain(schema []Def, values map[string]any) (map[string]any, []string) {
	idx := byKey(schema)
	kept := map[string]any{}
	var dropped []string
	for k, v := range values {
		if idx[k] != nil {
			kept[k] = v
		} else {
			dropped = append(dropped, k)
		}
	}
	sort.Strings(dropped)
	return kept, dropped
}

// Project returns only the values that belong to schema (used when rendering
// tasks, so orphaned values are never shown).
func Project(schema []Def, values map[string]any) map[string]any {
	kept, _ := Retain(schema, values)
	return kept
}
