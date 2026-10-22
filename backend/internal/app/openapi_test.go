package app

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/flowboard/backend/internal/identity"
	"github.com/flowboard/backend/openapi"
)

// TestOpenAPIMatchesRouter fails if a route exists in code but not in the spec,
// or the spec documents a path+method the router does not serve.
func TestOpenAPIMatchesRouter(t *testing.T) {
	r := New(nil, identity.Config{}).Router().(chi.Routes)
	served := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if strings.HasPrefix(route, "/v1") && method != http.MethodOptions {
			served[strings.ToLower(method)+" "+route] = true
		}
		return nil
	})

	spec := string(openapi.Spec)
	documented := map[string]bool{}
	pathRe := regexp.MustCompile(`(?m)^  (/v1[^:]*):\s*$`)
	methodRe := regexp.MustCompile(`(?m)^    (get|post|put|patch|delete):`)
	locs := pathRe.FindAllStringSubmatchIndex(spec, -1)
	for i, loc := range locs {
		end := len(spec)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if c := strings.Index(spec[loc[1]:end], "\ncomponents:"); c >= 0 {
			end = loc[1] + c
		}
		path := spec[loc[2]:loc[3]]
		for _, m := range methodRe.FindAllStringSubmatch(spec[loc[1]:end], -1) {
			documented[m[1]+" "+path] = true
		}
	}
	for k := range served {
		if !documented[k] {
			t.Errorf("route %q is served but missing from openapi.yaml", k)
		}
	}
	for k := range documented {
		if !served[k] {
			t.Errorf("openapi.yaml documents %q but the router does not serve it", k)
		}
	}
	if len(served) < 30 {
		t.Fatalf("walked only %d routes", len(served))
	}
}
