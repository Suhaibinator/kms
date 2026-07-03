package httpserver

import (
	"net/http"
	"strconv"
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

func invalidArg(msg string) error {
	return domain.Errorf(domain.ErrInvalidArgument, "%s", msg)
}
