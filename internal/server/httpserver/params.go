package httpserver

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// Request parsing helpers shared by the handlers. Resources are addressed by a
// flattened namespace (?env=&app=) plus a relative key (?key= for point ops,
// ?key_prefix= for lists). These helpers only read the raw wire values; format
// validation (keyutil) lives in the core service, which returns
// invalid_argument for malformed env/app/key.

// refFromQuery builds a domain.Ref from ?env=&app=&key= query params.
func refFromQuery(r *http.Request) domain.Ref {
	q := r.URL.Query()
	return domain.Ref{
		NS:  domain.NamespaceRef{Env: q.Get("env"), App: q.Get("app")},
		Key: q.Get("key"),
	}
}

// nsRefFromQuery builds a domain.NamespaceRef from ?env=&app= query params.
func nsRefFromQuery(r *http.Request) domain.NamespaceRef {
	q := r.URL.Query()
	return domain.NamespaceRef{Env: q.Get("env"), App: q.Get("app")}
}

// refFields is the flattened {env, app, key} address embedded in point-operation
// request bodies. Embed it in a handler's body struct to parse the address.
type refFields struct {
	Env string `json:"env"`
	App string `json:"app"`
	Key string `json:"key"`
}

func (f refFields) ref() domain.Ref {
	return domain.Ref{NS: domain.NamespaceRef{Env: f.Env, App: f.App}, Key: f.Key}
}

// listPage parses pagination query params.
func listPage(r *http.Request) storage.ListPage {
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	return storage.ListPage{Limit: size, Token: r.URL.Query().Get("page_token")}
}

// parseVersion parses a ?version= or body version. Empty means 0 (current).
func parseVersion(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, invalidArg("version must be a non-negative integer")
	}
	return v, nil
}

// parseUnixMS parses a Unix-millisecond timestamp query param; empty or 0 means
// the zero time.
func parseUnixMS(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, invalidArg("timestamp must be unix milliseconds")
	}
	if ms == 0 {
		return time.Time{}, nil
	}
	return time.UnixMilli(ms).UTC(), nil
}

// parseWindow parses an expiry look-ahead query param. It accepts a Go
// duration ("720h") or a bare "Nd" day count ("30d") — the two spellings the
// CLI's --ttl and --since already take — and an empty value means the default.
//
// The bounds are deliberately strict rather than clamped: zero and negative
// windows have no meaning here (a window that looks backwards would report
// certificates whose expiry is already being enforced by the handshake), and a
// look-ahead beyond a year stops distinguishing "soon" from "eventually" while
// asking the store to sort most of its history. Both are the caller's mistake,
// so they are refused instead of quietly rewritten.
func parseWindow(name, raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return postureDefaultWindow, nil
	}
	d, ok := parseDurationOrDays(raw)
	if !ok || d <= 0 || d > postureMaxWindow {
		return 0, invalidArg(name + ` must be a positive duration of at most 365d (for example "30d" or "720h")`)
	}
	return d, nil
}

// parseDurationOrDays accepts "720h" or "30d"; ok is false for anything else.
func parseDurationOrDays(raw string) (time.Duration, bool) {
	if days, found := strings.CutSuffix(raw, "d"); found {
		// Guard the "d" suffix against Go durations that legitimately end in
		// one, though none currently do: only a bare integer is a day count.
		if n, err := strconv.Atoi(days); err == nil {
			return time.Duration(n) * 24 * time.Hour, true
		}
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false
	}
	return d, true
}

func invalidArg(msg string) error {
	return domain.Errorf(domain.ErrInvalidArgument, "%s", msg)
}
