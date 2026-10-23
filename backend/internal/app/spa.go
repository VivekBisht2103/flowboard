package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// WithSPA serves the built frontend from dir on the same origin as the API:
// /v1/* and /healthz go to the API, existing files are served as-is (hashed
// assets cached for a year), and every other path falls back to index.html so
// client-side routes work. Used for single-container deployments.
func WithSPA(api http.Handler, dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/v1/") || p == "/v1" || p == "/healthz" {
			api.ServeHTTP(w, r)
			return
		}
		clean := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+p)))
		if st, err := os.Stat(clean); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}
