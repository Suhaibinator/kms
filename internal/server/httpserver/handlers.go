package httpserver

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// newAPIMux registers every JSON API route. Method-aware patterns give
// automatic 405 responses for the wrong verb.
func (s *server) newAPIMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)

	mux.HandleFunc("GET /api/v1/namespaces", s.handleListNamespaces)
	mux.HandleFunc("POST /api/v1/namespaces", s.handleCreateNamespace)

	mux.HandleFunc("GET /api/v1/parameters", s.handleListParameters)
	mux.HandleFunc("GET /api/v1/parameters/get", s.handleGetParameter)
	mux.HandleFunc("GET /api/v1/parameters/metadata", s.handleParameterMetadata)
	mux.HandleFunc("PUT /api/v1/parameters", s.handlePutParameter)
	mux.HandleFunc("DELETE /api/v1/parameters", s.handleDeleteParameter)

	mux.HandleFunc("GET /api/v1/secrets", s.handleListSecrets)
	mux.HandleFunc("GET /api/v1/secrets/metadata", s.handleSecretMetadata)
	mux.HandleFunc("POST /api/v1/secrets", s.handlePutSecret)
	mux.HandleFunc("POST /api/v1/secrets/reveal", s.handleRevealSecret)
	mux.HandleFunc("POST /api/v1/secrets/disable", s.handleDisableSecret)
	mux.HandleFunc("POST /api/v1/secrets/destroy", s.handleDestroySecret)
	mux.HandleFunc("POST /api/v1/secrets/promote", s.handlePromoteSecret)
	mux.HandleFunc("DELETE /api/v1/secrets", s.handleDeleteSecret)

	mux.HandleFunc("GET /api/v1/policies", s.handleListPolicies)
	mux.HandleFunc("POST /api/v1/policies", s.handleCreatePolicy)
	mux.HandleFunc("PUT /api/v1/policies", s.handleUpdatePolicy)
	mux.HandleFunc("DELETE /api/v1/policies", s.handleDeletePolicy)

	mux.HandleFunc("GET /api/v1/identities", s.handleListIdentities)
	mux.HandleFunc("POST /api/v1/identities", s.handleCreateIdentity)
	mux.HandleFunc("POST /api/v1/identities/rotate", s.handleRotateIdentity)
	mux.HandleFunc("POST /api/v1/identities/revoke", s.handleRevokeIdentity)

	mux.HandleFunc("GET /api/v1/audit", s.handleListAudit)
	mux.HandleFunc("GET /api/v1/subscribers", s.handleListSubscribers)
	mux.HandleFunc("GET /api/v1/keys", s.handleListKeys)

	return mux
}

// --- auth & health ---------------------------------------------------------

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	meta := metaFrom(r.Context())
	id, err := s.svc.Authenticate(r.Context(), body.Token, meta.ip, meta.ua)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity": map[string]string{"name": id.Name, "kind": id.Kind},
	})
}

// handleHealth is the API health endpoint (no auth).
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ready := s.svc.Ready(r.Context()) == nil
	var rev uint64
	if ready {
		rev, _ = s.svc.CurrentRevision(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"healthy":          true,
		"ready":            ready,
		"version":          s.version(),
		"current_revision": rev,
	})
}

func (s *server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := s.svc.Ready(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// --- namespaces ------------------------------------------------------------

func (s *server) handleListNamespaces(w http.ResponseWriter, r *http.Request) {
	pr := principalFrom(r.Context())
	items, next, err := s.svc.ListNamespaces(r.Context(), pr, listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]namespaceDTO, 0, len(items))
	for _, n := range items {
		out = append(out, toNamespaceDTO(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": out, "next_page_token": next})
}

func (s *server) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path        string `json:"path"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	ns, err := s.svc.CreateNamespace(r.Context(), principalFrom(r.Context()), body.Path, body.Description)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": toNamespaceDTO(ns)})
}

// --- parameters ------------------------------------------------------------

func (s *server) handleListParameters(w http.ResponseWriter, r *http.Request) {
	pr := principalFrom(r.Context())
	prefix := r.URL.Query().Get("prefix")
	items, next, err := s.svc.ListParameters(r.Context(), pr, prefix, listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]parameterDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toParameterDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"parameters": out, "next_page_token": next})
}

func (s *server) handleGetParameter(w http.ResponseWriter, r *http.Request) {
	pr := principalFrom(r.Context())
	q := r.URL.Query()
	version, err := parseVersion(q.Get("version"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	p, err := s.svc.GetParameter(r.Context(), pr, q.Get("path"), version, q.Get("label"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"parameter": toParameterDTO(p)})
}

func (s *server) handleParameterMetadata(w http.ResponseWriter, r *http.Request) {
	pr := principalFrom(r.Context())
	info, err := s.svc.GetParameterInfo(r.Context(), pr, r.URL.Query().Get("path"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toParameterInfoDTO(info))
}

func (s *server) handlePutParameter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path         string `json:"path"`
		Value        string `json:"value"`
		ContentType  string `json:"content_type"`
		MetadataJSON string `json:"metadata_json"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	version, revision, err := s.svc.PutParameter(r.Context(), principalFrom(r.Context()),
		body.Path, body.Value, body.ContentType, body.MetadataJSON)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "revision": revision})
}

func (s *server) handleDeleteParameter(w http.ResponseWriter, r *http.Request) {
	revision, err := s.svc.DeleteParameter(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("path"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

// --- secrets ---------------------------------------------------------------

func (s *server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	pr := principalFrom(r.Context())
	prefix := r.URL.Query().Get("prefix")
	items, next, err := s.svc.ListSecrets(r.Context(), pr, prefix, listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]secretMetadataDTO, 0, len(items))
	for _, sec := range items {
		out = append(out, toSecretMetadataDTO(sec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": out, "next_page_token": next})
}

func (s *server) handleSecretMetadata(w http.ResponseWriter, r *http.Request) {
	pr := principalFrom(r.Context())
	sec, err := s.svc.GetSecretInfo(r.Context(), pr, r.URL.Query().Get("path"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": toSecretMetadataDTO(sec)})
}

func (s *server) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path                string `json:"path"`
		ValueBase64         string `json:"value_base64"`
		ContentType         string `json:"content_type"`
		MetadataJSON        string `json:"metadata_json"`
		ClientBound         bool   `json:"client_bound"`
		GenerateAccessToken bool   `json:"generate_access_token"`
		ExpiresAtUnixMS     int64  `json:"expires_at_unix_ms"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	value, err := base64.StdEncoding.DecodeString(body.ValueBase64)
	if err != nil {
		s.writeError(w, r, invalidArg("value_base64 is not valid base64"))
		return
	}
	res, err := s.svc.PutSecret(r.Context(), principalFrom(r.Context()), core.PutSecretInput{
		Path:          body.Path,
		Value:         value,
		ContentType:   body.ContentType,
		Metadata:      body.MetadataJSON,
		ClientBound:   body.ClientBound,
		GenerateToken: body.GenerateAccessToken,
		ExpiresAt:     body.ExpiresAtUnixMS,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	resp := map[string]any{"version": res.Version, "revision": res.Revision}
	if res.AccessToken != "" {
		resp["access_token"] = res.AccessToken
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleRevealSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Version uint64 `json:"version"`
		Label   string `json:"label"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	val, err := s.svc.RevealSecret(r.Context(), principalFrom(r.Context()), body.Path, body.Version, body.Label)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":         val.Path,
		"version":      val.Version,
		"value_base64": base64.StdEncoding.EncodeToString(val.Value),
		"content_type": val.ContentType,
	})
}

func (s *server) handleDisableSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Version uint64 `json:"version"`
		Enable  bool   `json:"enable"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	revision, err := s.svc.DisableSecret(r.Context(), principalFrom(r.Context()), body.Path, body.Version, body.Enable)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func (s *server) handleDestroySecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Version uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	revision, err := s.svc.DestroySecretVersion(r.Context(), principalFrom(r.Context()), body.Path, body.Version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func (s *server) handlePromoteSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Version uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	current, previous, revision, err := s.svc.PromoteSecretVersion(r.Context(), principalFrom(r.Context()), body.Path, body.Version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current_version":  current,
		"previous_version": previous,
		"revision":         revision,
	})
}

func (s *server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	revision, err := s.svc.DeleteSecret(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("path"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

// --- policies --------------------------------------------------------------

func (s *server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	items, next, err := s.svc.ListPolicies(r.Context(), principalFrom(r.Context()), listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]policyDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toPolicyDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out, "next_page_token": next})
}

func (s *server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.decodePolicy(w, r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.svc.CreatePolicy(r.Context(), principalFrom(r.Context()), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": toPolicyDTO(out)})
}

func (s *server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.decodePolicy(w, r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.svc.UpdatePolicy(r.Context(), principalFrom(r.Context()), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": toPolicyDTO(out)})
}

func (s *server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeletePolicy(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("name")); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *server) decodePolicy(w http.ResponseWriter, r *http.Request) (domain.Policy, error) {
	var body struct {
		Policy policyDTO `json:"policy"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return domain.Policy{}, err
	}
	return body.Policy.toDomain(), nil
}

// --- identities ------------------------------------------------------------

func (s *server) handleListIdentities(w http.ResponseWriter, r *http.Request) {
	items, next, err := s.svc.ListIdentities(r.Context(), principalFrom(r.Context()), listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]identityDTO, 0, len(items))
	for _, id := range items {
		out = append(out, toIdentityDTO(id))
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": out, "next_page_token": next})
}

func (s *server) handleCreateIdentity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	id, token, err := s.svc.CreateIdentity(r.Context(), principalFrom(r.Context()), body.Name, body.Kind)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identity": toIdentityDTO(id), "token": token})
}

func (s *server) handleRotateIdentity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	token, err := s.svc.RotateIdentityToken(r.Context(), principalFrom(r.Context()), body.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

func (s *server) handleRevokeIdentity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.RevokeIdentity(r.Context(), principalFrom(r.Context()), body.Name); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// --- audit / subscribers / keys --------------------------------------------

func (s *server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := parseUnixMS(q.Get("from_unix_ms"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	to, err := parseUnixMS(q.Get("to_unix_ms"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	filter := domain.AuditFilter{
		PathPrefix:    q.Get("path_prefix"),
		ActorIdentity: q.Get("actor"),
		EventType:     q.Get("event_type"),
		From:          from,
		To:            to,
	}
	items, next, err := s.svc.ListAuditEvents(r.Context(), principalFrom(r.Context()), filter, listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]auditEventDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toAuditEventDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "next_page_token": next})
}

func (s *server) handleListSubscribers(w http.ResponseWriter, r *http.Request) {
	subs, rev, err := s.svc.ListSubscribers(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]subscriberDTO, 0, len(subs))
	for _, sub := range subs {
		out = append(out, toSubscriberDTO(sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscribers": out, "current_revision": rev})
}

func (s *server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.svc.ListKeyMetadata(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]keyDTO, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyDTO(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// --- shared parsing helpers ------------------------------------------------

func (s *server) version() string {
	if s.cfg.Version != "" {
		return s.cfg.Version
	}
	return s.svc.Version()
}

func listPage(r *http.Request) storage.ListPage {
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	return storage.ListPage{Limit: size, Token: r.URL.Query().Get("page_token")}
}

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
