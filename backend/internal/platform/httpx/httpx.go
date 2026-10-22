// Package httpx standardises the HTTP contract: JSON bodies, the error envelope
// { "error": { "code", "message", "details" } }, and cursor pagination.
package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// Error is the single error type surfaced to clients.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// FieldError points at the offending input by JSON path (e.g. "customFields.estimate").
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func E(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Message: msg} }

func (e *Error) With(details any) *Error { e.Details = details; return e }

func Unauthenticated(msg string) *Error { return E(http.StatusUnauthorized, "UNAUTHENTICATED", msg) }
func Forbidden(msg string) *Error       { return E(http.StatusForbidden, "FORBIDDEN", msg) }
func NotFound(what string) *Error       { return E(http.StatusNotFound, "NOT_FOUND", what+" not found") }
func BadRequest(msg string) *Error      { return E(http.StatusBadRequest, "BAD_REQUEST", msg) }
func Conflict(code, msg string) *Error  { return E(http.StatusConflict, code, msg) }

// Invalid builds a 400 VALIDATION_FAILED with per-field errors.
func Invalid(errs []FieldError) *Error {
	msg := "request validation failed"
	if len(errs) == 1 {
		msg = errs[0].Path + ": " + errs[0].Message
	}
	return E(http.StatusBadRequest, "VALIDATION_FAILED", msg).With(map[string]any{"fieldErrors": errs})
}

func InvalidField(path, code, msg string) *Error {
	return Invalid([]FieldError{{Path: path, Code: code, Message: msg}})
}

// WriteJSON writes v with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError maps any error to the envelope; unknown errors become 500 without leaking internals.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var he *Error
	if !errors.As(err, &he) {
		slog.Error("internal error", "path", r.URL.Path, "err", err)
		he = E(http.StatusInternalServerError, "INTERNAL", "internal server error")
	}
	WriteJSON(w, he.Status, map[string]any{"error": he})
}

// Handler adapts func(w, r) error into http.HandlerFunc.
type Handler func(w http.ResponseWriter, r *http.Request) error

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		WriteError(w, r, err)
	}
}

// Decode reads a JSON body strictly (unknown fields rejected) into dst.
func Decode(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return BadRequest("could not read body")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return InvalidField(ute.Field, "INVALID_TYPE", "expected "+ute.Type.String())
		}
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			return InvalidField(field, "UNKNOWN_FIELD", "unknown field")
		}
		return BadRequest("malformed JSON body: " + err.Error())
	}
	return nil
}

// Page is the standard list envelope.
type Page[T any] struct {
	Data []T      `json:"data"`
	Page PageInfo `json:"page"`
}

type PageInfo struct {
	NextCursor *string `json:"nextCursor"`
	Limit      int     `json:"limit"`
}

// Cursor is opaque to clients: base64url(JSON{ "v": sortValue, "id": lastId, "s": sortSignature }).
type Cursor struct {
	Value string `json:"v"`
	ID    string `json:"id"`
	Sort  string `json:"s"`
}

func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeCursor(s, sortSig string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, InvalidField("cursor", "INVALID_CURSOR", "malformed cursor")
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil || c.ID == "" {
		return nil, InvalidField("cursor", "INVALID_CURSOR", "malformed cursor")
	}
	if c.Sort != sortSig {
		return nil, InvalidField("cursor", "INVALID_CURSOR", "cursor was issued for a different sort order")
	}
	return &c, nil
}

// Limit parses ?limit= with a default and hard max.
func Limit(r *http.Request, def, max int) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > max {
		return 0, InvalidField("limit", "OUT_OF_RANGE", fmt.Sprintf("limit must be 1..%d", max))
	}
	return n, nil
}
