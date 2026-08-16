-- 0001_initial.sql — full v1 schema (namespace-native), DESCRIPTIVE ONLY.
-- The running schema is created by GORM AutoMigrate over the models in
-- models.go (plus the raw change_log DDL in store.go); this file documents
-- that schema and is not executed. Keep it in sync when models change.
-- Times are stored as RFC3339Nano UTC strings. All secret material in this
-- schema is ciphertext or hashes; plaintext secrets and raw keys never land here.
--
-- A namespace is a first-class (env, app) pair: parameters and secrets belong
-- to one by foreign key, keyed by a relative name (which may itself contain
-- '/'). Audit and change-log rows denormalize env/app/key as text (no FK) so
-- history stays readable after a namespace is deleted.

CREATE TABLE key_metadata (
    id         TEXT PRIMARY KEY,           -- e.g. "kek-a1b2c3"
    source     TEXT NOT NULL,              -- 'file' | 'passphrase'
    kdf        TEXT NOT NULL DEFAULT '',   -- JSON argon2id params when passphrase-derived
    kdf_salt   BLOB,
    key_check  BLOB NOT NULL,              -- nonce || AES-GCM(canary) under this KEK
    state      TEXT NOT NULL DEFAULT 'active', -- 'active' | 'retired'
    created_at TEXT NOT NULL
);

CREATE TABLE applications (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT NOT NULL UNIQUE,
    description    TEXT NOT NULL DEFAULT '',
    release_name   TEXT NOT NULL DEFAULT 'runtime',
    schema_id      TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL DEFAULT 0,
    contract_json  TEXT NOT NULL DEFAULT '[]',
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE TABLE namespaces (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    env                  TEXT NOT NULL,
    app                  TEXT NOT NULL REFERENCES applications(name),
    description          TEXT NOT NULL DEFAULT '',
    allowed_auth_methods TEXT NOT NULL DEFAULT '["mtls"]', -- JSON array: "mtls" | "token"
    created_by           TEXT NOT NULL DEFAULT '',
    created_at           TEXT NOT NULL,
    UNIQUE (env, app)
);

CREATE TABLE parameters (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id    INTEGER NOT NULL REFERENCES namespaces(id),
    name            TEXT NOT NULL,          -- relative key, e.g. 'rate-limit'
    content_type    TEXT NOT NULL DEFAULT 'string',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE (namespace_id, name)
);

CREATE TABLE parameter_versions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    parameter_id   INTEGER NOT NULL REFERENCES parameters(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL,
    value          TEXT NOT NULL,
    content_type   TEXT NOT NULL DEFAULT 'string',
    state          TEXT NOT NULL DEFAULT 'enabled',
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    metadata_json  TEXT NOT NULL DEFAULT '{}',
    UNIQUE (parameter_id, version_number)
);

CREATE TABLE parameter_labels (
    parameter_id   INTEGER NOT NULL REFERENCES parameters(id) ON DELETE CASCADE,
    label          TEXT NOT NULL,
    version_number INTEGER NOT NULL,
    PRIMARY KEY (parameter_id, label)
);

CREATE TABLE secrets (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace_id      INTEGER NOT NULL REFERENCES namespaces(id),
    name              TEXT NOT NULL,
    client_bound      INTEGER NOT NULL DEFAULT 0,
    access_token_hash BLOB,                -- sha256(token); NULL when no per-secret token
    content_type      TEXT NOT NULL DEFAULT 'application/octet-stream',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    UNIQUE (namespace_id, name)
);

CREATE TABLE secret_versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    secret_id       INTEGER NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
    version_number  INTEGER NOT NULL,
    ciphertext      BLOB,                  -- NULL after destroy
    encrypted_dek   BLOB,                  -- NULL after destroy
    kek_id          TEXT NOT NULL,
    wrap_mode       TEXT NOT NULL DEFAULT 'standard', -- 'standard' | 'client_bound'
    client_key_salt BLOB,                  -- HKDF salt for client_bound versions
    algorithm       TEXT NOT NULL DEFAULT 'AES-256-GCM',
    nonce           BLOB,                  -- value-encryption nonce; NULL after destroy
    aad             TEXT NOT NULL,         -- canonical associated-data string
    state           TEXT NOT NULL DEFAULT 'enabled', -- 'enabled' | 'disabled' | 'destroyed'
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    destroyed_at    TEXT,
    expires_at      TEXT,
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    UNIQUE (secret_id, version_number)
);

CREATE TABLE secret_labels (
    secret_id      INTEGER NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
    label          TEXT NOT NULL,
    version_number INTEGER NOT NULL,
    PRIMARY KEY (secret_id, label)
);

CREATE TABLE identities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    kind          TEXT NOT NULL,           -- 'client' | 'admin'
    token_hash    BLOB,                    -- sha256(token); NULL for cert-only identities
    namespace_id  INTEGER REFERENCES namespaces(id), -- NULL = unbound (admin/tooling)
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_identities_token_hash ON identities(token_hash);

-- Built-in CA. Private key material is encrypted under the active KEK exactly
-- like a secret version; the public cert is served at GET /api/v1/ca.
CREATE TABLE ca_keys (
    id            TEXT PRIMARY KEY,        -- e.g. "ca-7f3a"
    cert_pem      TEXT NOT NULL,
    encrypted_key BLOB NOT NULL,
    encrypted_dek BLOB NOT NULL,
    kek_id        TEXT NOT NULL,           -- id of the wrapping KEK (no FK at runtime)
    state         TEXT NOT NULL DEFAULT 'active', -- 'active' | 'retired'
    created_at    TEXT NOT NULL
);

-- Issued client certificates (never private keys — those are returned once).
CREATE TABLE identity_certs (
    serial      TEXT PRIMARY KEY,
    identity_id INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,             -- sha256 of DER, for display/pinning
    not_after   TEXT NOT NULL,
    revoked_at  TEXT,                       -- NULL = valid
    created_at  TEXT NOT NULL
);

CREATE INDEX idx_identity_certs_identity ON identity_certs(identity_id);

CREATE TABLE policies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    subject    TEXT NOT NULL,              -- identity name or '*'
    rules_json TEXT NOT NULL,              -- {"allow":[{"operation","env","app","key"}],"deny":[...]}
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_policies_subject ON policies(subject);

CREATE TABLE audit_events (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type       TEXT NOT NULL,
    actor_identity   TEXT NOT NULL DEFAULT '',
    actor_type       TEXT NOT NULL DEFAULT '',
    resource_type    TEXT NOT NULL DEFAULT '',
    resource_namespace_id INTEGER NOT NULL DEFAULT 0,
    resource_env     TEXT NOT NULL DEFAULT '',
    resource_app     TEXT NOT NULL DEFAULT '',
    resource_key     TEXT NOT NULL DEFAULT '',
    resource_version INTEGER NOT NULL DEFAULT 0,
    decision         TEXT NOT NULL DEFAULT '',
    source_ip        TEXT NOT NULL DEFAULT '',
    user_agent       TEXT NOT NULL DEFAULT '',
    request_id       TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    metadata_json    TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_audit_created ON audit_events(created_at);
CREATE INDEX idx_audit_ns ON audit_events(resource_env, resource_app);
CREATE INDEX idx_audit_namespace_id ON audit_events(resource_namespace_id);
CREATE INDEX idx_audit_actor ON audit_events(actor_identity);

-- change_log is the watch replay journal. AUTOINCREMENT guarantees revisions
-- stay monotonic and are never reused even after pruning (sqlite_sequence).
CREATE TABLE change_log (
    revision      INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type TEXT NOT NULL,           -- 'parameter' | 'secret' | 'configuration_release'
    namespace_id  INTEGER NOT NULL DEFAULT 0,
    env           TEXT NOT NULL,
    app           TEXT NOT NULL,
    key           TEXT NOT NULL,
    change_type   TEXT NOT NULL,           -- put|delete|label|promote|disable|enable|destroy
    value         TEXT,                    -- new value for parameter puts; NULL for secrets
    content_type  TEXT NOT NULL DEFAULT '',
    version_number INTEGER NOT NULL DEFAULT 0,
    label         TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);

CREATE INDEX idx_change_log_ns ON change_log(env, app);
CREATE INDEX idx_change_log_namespace_revision ON change_log(namespace_id, revision);
