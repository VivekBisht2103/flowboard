// Package identity is the auth boundary. It turns request credentials into a
// server-side User loaded from the database. Nothing downstream trusts roles or
// permissions sent by the client.
//
// Accepted credentials (mock identity for the take-home):
//   - Authorization: Bearer <JWT HS256 { userId, role, exp }>  (short-lived, issued by POST /v1/auth/token)
//   - X-User-Id: alice | bob | carol                          (dev convenience; can be disabled)
//
// The JWT's role claim is informational only: the role is always re-read from the
// users table, so a forged or stale role claim grants nothing.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/platform/httpx"
)

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

type ctxKey struct{}

func FromContext(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

type Config struct {
	JWTSecret       []byte
	TokenTTL        time.Duration
	AllowHeaderAuth bool // accept X-User-Id
	EnableDevIssuer bool // expose POST /v1/auth/token
}

type Service struct {
	DB  db.Q
	Cfg Config
}

func (s *Service) Lookup(ctx context.Context, q db.Q, id string) (*User, error) {
	var u User
	err := q.QueryRowContext(ctx, `SELECT id, name, email, role FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Name, &u.Email, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, email, role FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Authenticate resolves the caller or returns a 401 error.
func (s *Service) Authenticate(r *http.Request) (*User, error) {
	var tokenUser, headerUser string
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return nil, httpx.Unauthenticated("Authorization header must be 'Bearer <token>'")
		}
		claims, err := s.Verify(tok)
		if err != nil {
			return nil, httpx.Unauthenticated(err.Error())
		}
		tokenUser = claims.UserID
	}
	if h := strings.TrimSpace(r.Header.Get("X-User-Id")); h != "" {
		if !s.Cfg.AllowHeaderAuth {
			return nil, httpx.Unauthenticated("X-User-Id authentication is disabled")
		}
		headerUser = h
	}
	if tokenUser != "" && headerUser != "" && tokenUser != headerUser {
		return nil, httpx.Unauthenticated("X-User-Id does not match the bearer token subject")
	}
	id := tokenUser
	if id == "" {
		id = headerUser
	}
	if id == "" {
		return nil, httpx.Unauthenticated("missing credentials: send X-User-Id or Authorization: Bearer <jwt>")
	}
	u, err := s.Lookup(r.Context(), s.DB, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, httpx.Unauthenticated("unknown user")
	}
	return u, nil
}

// Middleware rejects unauthenticated requests and stores the User in context.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.Authenticate(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// ---- Minimal HS256 JWT (stdlib only) ----

type Claims struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
}

var b64 = base64.RawURLEncoding

func (s *Service) Sign(c Claims) string {
	header := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, _ := json.Marshal(c)
	unsigned := header + "." + b64.EncodeToString(body)
	m := hmac.New(sha256.New, s.Cfg.JWTSecret)
	m.Write([]byte(unsigned))
	return unsigned + "." + b64.EncodeToString(m.Sum(nil))
}

func (s *Service) Verify(tok string) (*Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	hdr, err := b64.DecodeString(parts[0])
	if err != nil || !strings.Contains(string(hdr), `"HS256"`) {
		return nil, errors.New("unsupported token algorithm")
	}
	m := hmac.New(sha256.New, s.Cfg.JWTSecret)
	m.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, m.Sum(nil)) {
		return nil, errors.New("invalid token signature")
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed token")
	}
	var c Claims
	if err := json.Unmarshal(body, &c); err != nil || c.UserID == "" {
		return nil, errors.New("malformed token claims")
	}
	if time.Now().Unix() >= c.Exp {
		return nil, errors.New("token expired")
	}
	return &c, nil
}

// IssueHandler is the dev-only token endpoint: POST /v1/auth/token { "userId": "bob" }.
// In production this is replaced by an OIDC provider; see README "Auth".
func (s *Service) IssueHandler(w http.ResponseWriter, r *http.Request) error {
	if !s.Cfg.EnableDevIssuer {
		return httpx.NotFound("route")
	}
	var in struct {
		UserID string `json:"userId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := s.Lookup(r.Context(), s.DB, in.UserID)
	if err != nil {
		return err
	}
	if u == nil {
		return httpx.Unauthenticated("unknown user")
	}
	now := time.Now()
	exp := now.Add(s.Cfg.TokenTTL)
	tok := s.Sign(Claims{UserID: u.ID, Role: u.Role, Iat: now.Unix(), Exp: exp.Unix()})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"token": tok, "tokenType": "Bearer", "expiresAt": exp.UTC().Format(time.RFC3339), "user": u})
	return nil
}
