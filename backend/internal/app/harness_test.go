package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/flowboard/backend/internal/app"
	"github.com/flowboard/backend/internal/events"
	"github.com/flowboard/backend/internal/identity"
	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/seed"
)

// Integration tests run against a real Postgres (TEST_DATABASE_URL). Each test
// reseeds the database, so tests are order-independent (but not parallel).

var (
	testDB  *sql.DB
	testApp *app.App
	srv     *httptest.Server
)

const jwtSecret = "test-secret"

func TestMain(m *testing.M) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Println("TEST_DATABASE_URL not set; integration tests will be skipped")
		os.Exit(m.Run())
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	var err error
	if testDB, err = db.Open(ctx, url); err != nil {
		panic(err)
	}
	if err := db.Migrate(ctx, testDB); err != nil {
		panic(err)
	}
	testApp = app.New(testDB, identity.Config{JWTSecret: []byte(jwtSecret), TokenTTL: time.Minute, AllowHeaderAuth: true, EnableDevIssuer: true})
	srv = httptest.NewServer(testApp.Router())
	code := m.Run()
	srv.Close()
	os.Exit(code)
}

func reset(t *testing.T) {
	t.Helper()
	if testDB == nil {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := seed.Run(context.Background(), testDB, true); err != nil {
		t.Fatal(err)
	}
}

type resp struct {
	Status int
	Body   map[string]any
	Raw    []byte
}

// errCode / fieldErrors helpers read the standard error envelope.
func (r resp) errCode() string {
	if e, ok := r.Body["error"].(map[string]any); ok {
		return e["code"].(string)
	}
	return ""
}

func (r resp) fieldErrors() []map[string]any {
	e, _ := r.Body["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	fe, _ := d["fieldErrors"].([]any)
	var out []map[string]any
	for _, x := range fe {
		out = append(out, x.(map[string]any))
	}
	return out
}

func (r resp) hasFieldError(path, code string) bool {
	for _, fe := range r.fieldErrors() {
		if fe["path"] == path && fe["code"] == code {
			return true
		}
	}
	return false
}

func call(t *testing.T, user, method, path string, body any, headers ...string) resp {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+"/v1"+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw, Body: map[string]any{}}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func expect(t *testing.T, r resp, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("expected HTTP %d, got %d: %s", status, r.Status, r.Raw)
	}
}

// boardColumns returns the ordered status keys of a list's board.
func boardColumns(t *testing.T, user, listID string) ([]string, map[string]any) {
	t.Helper()
	r := call(t, user, "GET", "/lists/"+listID+"/board", nil)
	expect(t, r, 200)
	var keys []string
	for _, c := range r.Body["columns"].([]any) {
		keys = append(keys, c.(map[string]any)["status"].(map[string]any)["key"].(string))
	}
	return keys, r.Body
}

func getTask(t *testing.T, user, id string) map[string]any {
	t.Helper()
	r := call(t, user, "GET", "/tasks/"+id, nil)
	expect(t, r, 200)
	return r.Body
}

func cf(task map[string]any) map[string]any { return task["customFields"].(map[string]any) }

func drainOutbox(t *testing.T) int {
	t.Helper()
	w := &events.Worker{DB: testDB, Consumers: []events.Consumer{events.ActivityProjector{}}}
	total := 0
	for {
		n, err := w.Tick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return total
		}
		total += n
	}
}
