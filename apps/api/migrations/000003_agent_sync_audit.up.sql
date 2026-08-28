BEGIN;

CREATE TABLE dayorder.agent_runs (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    intent TEXT NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'ready',
    action_mode VARCHAR(16) NOT NULL,
    scope JSONB NOT NULL DEFAULT '[]'::jsonb,
    provider VARCHAR(80),
    model VARCHAR(120),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    summary TEXT,
    error_code VARCHAR(80),
    error_message TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_runs_intent_check CHECK (length(btrim(intent)) > 0),
    CONSTRAINT agent_runs_status_check CHECK (
        status IN ('ready', 'reading', 'analyzing', 'waiting', 'applying', 'completed', 'failed', 'stopped')
    ),
    CONSTRAINT agent_runs_action_mode_check CHECK (action_mode IN ('read', 'confirm')),
    CONSTRAINT agent_runs_scope_check CHECK (jsonb_typeof(scope) IN ('array', 'object')),
    CONSTRAINT agent_runs_times_check CHECK (finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at),
    CONSTRAINT agent_runs_version_check CHECK (version > 0),
    CONSTRAINT agent_runs_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX agent_runs_user_created_idx ON dayorder.agent_runs (user_id, created_at DESC, id);
CREATE INDEX agent_runs_user_active_idx
    ON dayorder.agent_runs (user_id, status, updated_at DESC)
    WHERE status IN ('ready', 'reading', 'analyzing', 'waiting', 'applying');

CREATE TABLE dayorder.agent_steps (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    run_id UUID NOT NULL,
    sequence_no INTEGER NOT NULL,
    title VARCHAR(240) NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    status VARCHAR(24) NOT NULL DEFAULT 'pending',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_steps_run_fk FOREIGN KEY (user_id, run_id)
        REFERENCES dayorder.agent_runs(user_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_steps_sequence_check CHECK (sequence_no > 0),
    CONSTRAINT agent_steps_title_check CHECK (length(btrim(title)) BETWEEN 1 AND 240),
    CONSTRAINT agent_steps_status_check CHECK (status IN ('pending', 'running', 'done', 'failed')),
    CONSTRAINT agent_steps_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT agent_steps_times_check CHECK (finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at),
    CONSTRAINT agent_steps_version_check CHECK (version > 0),
    CONSTRAINT agent_steps_user_pair_unique UNIQUE (user_id, id),
    CONSTRAINT agent_steps_run_sequence_unique UNIQUE (user_id, run_id, sequence_no)
);

CREATE TABLE dayorder.agent_changes (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    run_id UUID NOT NULL,
    change_type VARCHAR(40) NOT NULL,
    target_type VARCHAR(32) NOT NULL,
    target_id UUID,
    base_version BIGINT,
    patch JSONB NOT NULL,
    preview_before JSONB,
    preview_after JSONB,
    reason TEXT NOT NULL DEFAULT '',
    status VARCHAR(24) NOT NULL DEFAULT 'pending',
    accepted_at TIMESTAMPTZ,
    applied_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_changes_run_fk FOREIGN KEY (user_id, run_id)
        REFERENCES dayorder.agent_runs(user_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_changes_type_check CHECK (
        change_type IN ('reschedule-task', 'create-task', 'create-event', 'archive-record', 'link-note')
    ),
    CONSTRAINT agent_changes_target_type_check CHECK (
        target_type IN ('goal', 'milestone', 'task', 'calendar_event', 'record', 'note', 'daily_review')
    ),
    CONSTRAINT agent_changes_base_version_check CHECK (base_version IS NULL OR base_version > 0),
    CONSTRAINT agent_changes_patch_check CHECK (jsonb_typeof(patch) = 'array'),
    CONSTRAINT agent_changes_preview_before_check CHECK (
        preview_before IS NULL OR jsonb_typeof(preview_before) = 'object'
    ),
    CONSTRAINT agent_changes_preview_after_check CHECK (
        preview_after IS NULL OR jsonb_typeof(preview_after) = 'object'
    ),
    CONSTRAINT agent_changes_status_check CHECK (
        status IN ('pending', 'accepted', 'rejected', 'applied', 'failed', 'conflicted')
    ),
    CONSTRAINT agent_changes_version_check CHECK (version > 0),
    CONSTRAINT agent_changes_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX agent_changes_run_status_idx
    ON dayorder.agent_changes (user_id, run_id, status, created_at);

CREATE TABLE dayorder.agent_source_refs (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    run_id UUID NOT NULL,
    entity_type VARCHAR(32) NOT NULL,
    entity_id UUID NOT NULL,
    entity_version BIGINT NOT NULL,
    label_snapshot VARCHAR(240) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_source_refs_run_fk FOREIGN KEY (user_id, run_id)
        REFERENCES dayorder.agent_runs(user_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_source_refs_entity_type_check CHECK (
        entity_type IN ('goal', 'milestone', 'task', 'calendar_event', 'record', 'note', 'daily_review')
    ),
    CONSTRAINT agent_source_refs_entity_version_check CHECK (entity_version > 0),
    CONSTRAINT agent_source_refs_label_check CHECK (length(btrim(label_snapshot)) BETWEEN 1 AND 240),
    CONSTRAINT agent_source_refs_user_pair_unique UNIQUE (user_id, id),
    CONSTRAINT agent_source_refs_unique UNIQUE (user_id, run_id, entity_type, entity_id, entity_version)
);

CREATE TABLE dayorder.audit_events (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    actor_type VARCHAR(16) NOT NULL,
    actor_id UUID,
    action VARCHAR(120) NOT NULL,
    request_id UUID NOT NULL,
    before_data JSONB,
    after_data JSONB,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT audit_events_actor_type_check CHECK (actor_type IN ('user', 'agent', 'system')),
    CONSTRAINT audit_events_action_check CHECK (length(btrim(action)) BETWEEN 1 AND 120),
    CONSTRAINT audit_events_before_data_check CHECK (
        before_data IS NULL OR jsonb_typeof(before_data) = 'object'
    ),
    CONSTRAINT audit_events_after_data_check CHECK (
        after_data IS NULL OR jsonb_typeof(after_data) = 'object'
    ),
    CONSTRAINT audit_events_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT audit_events_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX audit_events_user_created_idx
    ON dayorder.audit_events (user_id, created_at DESC, id);
CREATE INDEX audit_events_request_idx
    ON dayorder.audit_events (user_id, request_id, created_at);

CREATE TABLE dayorder.audit_event_entities (
    audit_event_id UUID NOT NULL,
    user_id UUID NOT NULL,
    entity_type VARCHAR(32) NOT NULL,
    entity_id UUID NOT NULL,
    PRIMARY KEY (user_id, audit_event_id, entity_type, entity_id),
    CONSTRAINT audit_event_entities_event_fk FOREIGN KEY (user_id, audit_event_id)
        REFERENCES dayorder.audit_events(user_id, id) ON DELETE CASCADE,
    CONSTRAINT audit_event_entities_type_check CHECK (
        entity_type IN ('goal', 'milestone', 'task', 'calendar_event', 'record', 'note', 'daily_review', 'agent_run')
    )
);

CREATE INDEX audit_event_entities_entity_idx
    ON dayorder.audit_event_entities (user_id, entity_type, entity_id, audit_event_id);

CREATE TABLE dayorder.sync_changes (
    sequence BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    entity_type VARCHAR(32) NOT NULL,
    entity_id UUID NOT NULL,
    operation VARCHAR(16) NOT NULL,
    entity_version BIGINT NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sync_changes_entity_type_check CHECK (
        entity_type IN ('goal', 'milestone', 'task', 'calendar_event', 'reminder', 'record', 'note', 'daily_review', 'tag', 'agent_run', 'agent_change', 'settings')
    ),
    CONSTRAINT sync_changes_operation_check CHECK (operation IN ('create', 'update', 'delete')),
    CONSTRAINT sync_changes_entity_version_check CHECK (entity_version > 0),
    CONSTRAINT sync_changes_user_sequence_unique UNIQUE (user_id, sequence)
);

CREATE INDEX sync_changes_user_changed_idx
    ON dayorder.sync_changes (user_id, changed_at, sequence);

CREATE TABLE dayorder.client_mutations (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    device_id UUID NOT NULL,
    mutation_id UUID NOT NULL,
    request_hash BYTEA NOT NULL,
    response_status INTEGER,
    response_body JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT client_mutations_device_fk FOREIGN KEY (user_id, device_id)
        REFERENCES dayorder.user_devices(user_id, id) ON DELETE CASCADE,
    CONSTRAINT client_mutations_response_status_check CHECK (
        response_status IS NULL OR response_status BETWEEN 100 AND 599
    ),
    CONSTRAINT client_mutations_response_body_check CHECK (
        response_body IS NULL OR jsonb_typeof(response_body) IN ('object', 'array')
    ),
    CONSTRAINT client_mutations_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT client_mutations_identity_unique UNIQUE (user_id, device_id, mutation_id)
);

CREATE INDEX client_mutations_expiry_idx ON dayorder.client_mutations (expires_at);

CREATE TABLE dayorder.outbox_events (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    event_type VARCHAR(120) NOT NULL,
    aggregate_type VARCHAR(32) NOT NULL,
    aggregate_id UUID NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(24) NOT NULL DEFAULT 'pending',
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0,
    locked_at TIMESTAMPTZ,
    lock_token UUID,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    CONSTRAINT outbox_events_event_type_check CHECK (length(btrim(event_type)) BETWEEN 1 AND 120),
    CONSTRAINT outbox_events_aggregate_type_check CHECK (length(btrim(aggregate_type)) BETWEEN 1 AND 32),
    CONSTRAINT outbox_events_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_events_status_check CHECK (status IN ('pending', 'processing', 'processed', 'dead')),
    CONSTRAINT outbox_events_attempts_check CHECK (attempts >= 0),
    CONSTRAINT outbox_events_lock_check CHECK (
        (status = 'processing' AND locked_at IS NOT NULL AND lock_token IS NOT NULL)
        OR (status <> 'processing' AND locked_at IS NULL AND lock_token IS NULL)
    ),
    CONSTRAINT outbox_events_processed_check CHECK (
        (status = 'processed' AND processed_at IS NOT NULL) OR status <> 'processed'
    ),
    CONSTRAINT outbox_events_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX outbox_events_claim_idx
    ON dayorder.outbox_events (available_at, created_at, id)
    WHERE status = 'pending';
CREATE INDEX outbox_events_stale_lock_idx
    ON dayorder.outbox_events (locked_at, id)
    WHERE status = 'processing';
CREATE INDEX outbox_events_user_aggregate_idx
    ON dayorder.outbox_events (user_id, aggregate_type, aggregate_id, created_at DESC);

COMMIT;
