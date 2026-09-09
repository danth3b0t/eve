-- EVE v0.1 initial state schema. Application-level invariants are in SPEC.md.
-- Configure connection pragmas on every connection as required.
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
PRAGMA busy_timeout = 5000;

BEGIN IMMEDIATE;

CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at_ms INTEGER NOT NULL
);

CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    common_dir TEXT NOT NULL UNIQUE,
    source_path TEXT NOT NULL,
    label TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL
);

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    branch TEXT NOT NULL,
    path TEXT NOT NULL,
    git_admin_dir TEXT,
    head_oid TEXT NOT NULL,
    manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json)),
    manifest_sha256 TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    state TEXT NOT NULL CHECK (state IN (
        'creating','prepared','syncing','failed','expired','remote_missing',
        'destroying','cleanup_pending','destroyed'
    )),
    phase TEXT NOT NULL,
    owns_worktree INTEGER NOT NULL DEFAULT 1 CHECK (owns_worktree = 1),
    diagnostic_code TEXT,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    destroyed_at_ms INTEGER
);
CREATE UNIQUE INDEX workspaces_live_path ON workspaces(path)
    WHERE state <> 'destroyed';
CREATE UNIQUE INDEX workspaces_live_branch ON workspaces(repository_id, branch)
    WHERE state <> 'destroyed';

CREATE TABLE credential_objects (
    id TEXT PRIMARY KEY,
    -- Opaque relative ID under the protected secret store, not credential text.
    secret_object_ref TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK (kind IN ('management_token','deployment_key','hmac_key')),
    provider TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    created_at_ms INTEGER NOT NULL,
    expires_at_ms INTEGER,
    deleted_at_ms INTEGER
);

CREATE TABLE credential_profiles (
    provider TEXT NOT NULL,
    name TEXT NOT NULL,
    credential_id TEXT NOT NULL REFERENCES credential_objects(id),
    team_id TEXT NOT NULL,
    team_slug TEXT,
    last_validated_at_ms INTEGER,
    PRIMARY KEY (provider, name)
);

CREATE TABLE port_blocks (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
    base INTEGER NOT NULL CHECK (base BETWEEN 1 AND 65535),
    size INTEGER NOT NULL CHECK (size BETWEEN 1 AND 1000),
    CHECK (base + size - 1 <= 65535)
);

CREATE TABLE port_claims (
    port INTEGER PRIMARY KEY CHECK (port BETWEEN 1 AND 65535),
    workspace_id TEXT NOT NULL REFERENCES port_blocks(workspace_id),
    claimed_at_ms INTEGER NOT NULL,
    UNIQUE (workspace_id, port)
);
CREATE TRIGGER port_claim_within_block BEFORE INSERT ON port_claims
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM port_blocks b
        WHERE b.workspace_id = NEW.workspace_id
          AND NEW.port >= b.base AND NEW.port < b.base + b.size
    ) THEN RAISE(ABORT, 'port outside workspace block') END;
END;

CREATE TABLE endpoints (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    service_id TEXT NOT NULL,
    name TEXT NOT NULL,
    slot INTEGER NOT NULL CHECK (slot >= 0),
    port INTEGER NOT NULL,
    env_key TEXT NOT NULL,
    host TEXT NOT NULL,
    scheme TEXT NOT NULL CHECK (scheme IN ('http','https')),
    PRIMARY KEY (workspace_id, service_id, name),
    UNIQUE (workspace_id, slot),
    FOREIGN KEY (workspace_id, port) REFERENCES port_claims(workspace_id, port)
);
CREATE TRIGGER endpoint_matches_slot BEFORE INSERT ON endpoints
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM port_blocks b
        WHERE b.workspace_id = NEW.workspace_id
          AND NEW.slot < b.size AND NEW.port = b.base + NEW.slot
    ) THEN RAISE(ABORT, 'endpoint does not match allocated slot') END;
END;

CREATE TRIGGER port_claim_no_reassignment BEFORE UPDATE OF port, workspace_id ON port_claims
BEGIN
    SELECT RAISE(ABORT, 'replace port claims transactionally; do not reassign in place');
END;
CREATE TRIGGER endpoint_no_reallocation BEFORE UPDATE OF port, slot, workspace_id ON endpoints
BEGIN
    SELECT RAISE(ABORT, 'endpoint allocation is immutable');
END;
CREATE TRIGGER block_no_resize BEFORE UPDATE OF base, size, workspace_id ON port_blocks
BEGIN
    SELECT RAISE(ABORT, 'workspace port block is immutable');
END;

CREATE TABLE resources (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    resource_key TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider = 'convex'),
    spec_json TEXT NOT NULL CHECK (json_valid(spec_json)),
    remote_reference TEXT NOT NULL,
    remote_project_id TEXT,
    remote_id TEXT,
    remote_name TEXT,
    credential_id TEXT REFERENCES credential_objects(id),
    key_generation INTEGER NOT NULL DEFAULT 0,
    intended_expires_at_ms INTEGER,
    expires_at_ms INTEGER,
    last_observed_at_ms INTEGER,
    state TEXT NOT NULL CHECK (state IN (
        'planned','provisioning','unknown','provisioned','configuring','configured',
        'failed','expired','missing','deleting','cleanup_pending','deleted'
    )),
    outputs_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(outputs_json)),
    owned_env_hmacs_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(owned_env_hmacs_json)),
    UNIQUE (workspace_id, resource_key)
);
CREATE UNIQUE INDEX resources_exact_remote ON resources(provider, remote_project_id, remote_id)
    WHERE remote_id IS NOT NULL AND state <> 'deleted';

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    supersedes_operation_id TEXT REFERENCES operations(id),
    command TEXT NOT NULL CHECK (command IN ('create','sync','destroy','gc')),
    state TEXT NOT NULL CHECK (state IN ('pending','inflight','succeeded','failed','unknown','cancelled')),
    phase TEXT NOT NULL,
    intent_json TEXT NOT NULL CHECK (json_valid(intent_json)),
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    finished_at_ms INTEGER
);
CREATE UNIQUE INDEX operations_one_unfinished ON operations(workspace_id)
    WHERE state NOT IN ('succeeded','cancelled');

CREATE TABLE operation_steps (
    operation_id TEXT NOT NULL REFERENCES operations(id),
    sequence INTEGER NOT NULL,
    action TEXT NOT NULL,
    subject TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','inflight','succeeded','failed','unknown')),
    request_metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(request_metadata_json)),
    outcome_metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(outcome_metadata_json)),
    provider_request_id TEXT,
    error_code TEXT,
    started_at_ms INTEGER,
    finished_at_ms INTEGER,
    PRIMARY KEY (operation_id, sequence)
);

CREATE TABLE managed_files (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    path TEXT NOT NULL,
    is_tracked INTEGER NOT NULL CHECK (is_tracked IN (0,1)),
    is_generated_only INTEGER NOT NULL CHECK (is_generated_only IN (0,1)),
    copy_source_path TEXT,
    published_content_hmac TEXT NOT NULL,
    published_generation INTEGER NOT NULL,
    mode INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, path)
);

CREATE TABLE managed_values (
    workspace_id TEXT NOT NULL,
    path TEXT NOT NULL,
    env_key TEXT NOT NULL,
    owners_json TEXT NOT NULL CHECK (json_valid(owners_json)),
    value_hmac TEXT NOT NULL,
    sensitivity TEXT NOT NULL CHECK (sensitivity IN ('public','private','credential')),
    PRIMARY KEY (workspace_id, path, env_key),
    FOREIGN KEY (workspace_id, path) REFERENCES managed_files(workspace_id, path)
);

CREATE TABLE file_transactions (
    operation_id TEXT NOT NULL REFERENCES operations(id),
    path TEXT NOT NULL,
    preimage_ref TEXT,
    staged_image_ref TEXT NOT NULL,
    preimage_hmac TEXT,
    staged_hmac TEXT NOT NULL,
    desired_mode INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('staged','published','verified','purged','conflict')),
    PRIMARY KEY (operation_id, path)
);

INSERT INTO schema_migrations(version, applied_at_ms)
VALUES (1, CAST(strftime('%s', 'now') AS INTEGER) * 1000);
COMMIT;

-- Do not update allocation rows in place: lifecycle operations insert/delete
-- claims transactionally. Persist historical allocation summaries in a
-- non-secret operation/tombstone before releasing live FK-constrained rows.
-- Delete endpoints before port_claims, then port_blocks.
-- JSON validity does not establish redaction; callers must pass safe DTOs.
