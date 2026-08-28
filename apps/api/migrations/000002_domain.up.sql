BEGIN;

CREATE TABLE dayorder.goals (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    title VARCHAR(240) NOT NULL,
    why TEXT NOT NULL DEFAULT '',
    area VARCHAR(40) NOT NULL,
    metric_type VARCHAR(24) NOT NULL,
    target_value NUMERIC(20, 4) NOT NULL,
    current_value NUMERIC(20, 4) NOT NULL DEFAULT 0,
    unit VARCHAR(32) NOT NULL DEFAULT '',
    start_date DATE NOT NULL,
    due_date DATE,
    status VARCHAR(24) NOT NULL DEFAULT 'active',
    health VARCHAR(24) NOT NULL DEFAULT 'normal',
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT goals_title_check CHECK (length(btrim(title)) BETWEEN 1 AND 240),
    CONSTRAINT goals_area_check CHECK (length(btrim(area)) BETWEEN 1 AND 40),
    CONSTRAINT goals_metric_type_check CHECK (metric_type IN ('milestone', 'numeric', 'habit', 'project')),
    CONSTRAINT goals_target_value_check CHECK (target_value > 0),
    CONSTRAINT goals_current_value_check CHECK (current_value >= 0),
    CONSTRAINT goals_dates_check CHECK (due_date IS NULL OR due_date >= start_date),
    CONSTRAINT goals_status_check CHECK (status IN ('active', 'paused', 'completed', 'abandoned')),
    CONSTRAINT goals_health_check CHECK (health IN ('normal', 'attention', 'stalled')),
    CONSTRAINT goals_version_check CHECK (version > 0),
    CONSTRAINT goals_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX goals_user_status_idx
    ON dayorder.goals (user_id, status, updated_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX goals_user_due_date_idx
    ON dayorder.goals (user_id, due_date)
    WHERE deleted_at IS NULL AND due_date IS NOT NULL;

CREATE TABLE dayorder.goal_milestones (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    goal_id UUID NOT NULL,
    title VARCHAR(240) NOT NULL,
    due_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    sort_order INTEGER NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT goal_milestones_goal_fk FOREIGN KEY (user_id, goal_id)
        REFERENCES dayorder.goals(user_id, id) ON DELETE CASCADE,
    CONSTRAINT goal_milestones_title_check CHECK (length(btrim(title)) BETWEEN 1 AND 240),
    CONSTRAINT goal_milestones_sort_order_check CHECK (sort_order >= 0),
    CONSTRAINT goal_milestones_version_check CHECK (version > 0),
    CONSTRAINT goal_milestones_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX goal_milestones_goal_sort_idx
    ON dayorder.goal_milestones (user_id, goal_id, sort_order, created_at)
    WHERE deleted_at IS NULL;

CREATE TABLE dayorder.records (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    raw_text TEXT NOT NULL,
    kind VARCHAR(24) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    mood SMALLINT,
    energy SMALLINT,
    archived_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT records_raw_text_check CHECK (length(btrim(raw_text)) > 0),
    CONSTRAINT records_kind_check CHECK (kind IN ('status', 'idea', 'completion', 'inbox')),
    CONSTRAINT records_mood_check CHECK (mood IS NULL OR mood BETWEEN 1 AND 5),
    CONSTRAINT records_energy_check CHECK (energy IS NULL OR energy BETWEEN 1 AND 5),
    CONSTRAINT records_version_check CHECK (version > 0),
    CONSTRAINT records_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX records_user_occurred_idx
    ON dayorder.records (user_id, occurred_at DESC, id)
    WHERE deleted_at IS NULL;
CREATE INDEX records_user_active_idx
    ON dayorder.records (user_id, updated_at DESC)
    WHERE deleted_at IS NULL AND archived_at IS NULL;

CREATE TABLE dayorder.tasks (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    title VARCHAR(240) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'todo',
    priority VARCHAR(24) NOT NULL DEFAULT 'normal',
    estimate_minutes INTEGER NOT NULL DEFAULT 0,
    due_at TIMESTAMPTZ,
    scheduled_start TIMESTAMPTZ,
    scheduled_end TIMESTAMPTZ,
    goal_id UUID,
    source_record_id UUID,
    completed_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT tasks_goal_fk FOREIGN KEY (user_id, goal_id)
        REFERENCES dayorder.goals(user_id, id) ON DELETE RESTRICT,
    CONSTRAINT tasks_source_record_fk FOREIGN KEY (user_id, source_record_id)
        REFERENCES dayorder.records(user_id, id) ON DELETE RESTRICT,
    CONSTRAINT tasks_title_check CHECK (length(btrim(title)) BETWEEN 1 AND 240),
    CONSTRAINT tasks_status_check CHECK (status IN ('todo', 'doing', 'done', 'archived')),
    CONSTRAINT tasks_priority_check CHECK (priority IN ('normal', 'important')),
    CONSTRAINT tasks_estimate_minutes_check CHECK (estimate_minutes >= 0),
    CONSTRAINT tasks_schedule_check CHECK (
        scheduled_end IS NULL OR (scheduled_start IS NOT NULL AND scheduled_end >= scheduled_start)
    ),
    CONSTRAINT tasks_completion_check CHECK (status <> 'done' OR completed_at IS NOT NULL),
    CONSTRAINT tasks_version_check CHECK (version > 0),
    CONSTRAINT tasks_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX tasks_user_status_idx
    ON dayorder.tasks (user_id, status, updated_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX tasks_user_due_idx
    ON dayorder.tasks (user_id, due_at, id)
    WHERE deleted_at IS NULL AND due_at IS NOT NULL;
CREATE INDEX tasks_user_schedule_idx
    ON dayorder.tasks (user_id, scheduled_start, scheduled_end)
    WHERE deleted_at IS NULL AND scheduled_start IS NOT NULL;
CREATE INDEX tasks_user_goal_idx
    ON dayorder.tasks (user_id, goal_id)
    WHERE deleted_at IS NULL AND goal_id IS NOT NULL;

CREATE TABLE dayorder.calendar_events (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    title VARCHAR(240) NOT NULL,
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    location VARCHAR(240),
    kind VARCHAR(24) NOT NULL,
    source_calendar VARCHAR(120),
    goal_id UUID,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT calendar_events_goal_fk FOREIGN KEY (user_id, goal_id)
        REFERENCES dayorder.goals(user_id, id) ON DELETE RESTRICT,
    CONSTRAINT calendar_events_title_check CHECK (length(btrim(title)) BETWEEN 1 AND 240),
    CONSTRAINT calendar_events_times_check CHECK (end_at >= start_at),
    CONSTRAINT calendar_events_timezone_check CHECK (length(btrim(timezone)) BETWEEN 1 AND 64),
    CONSTRAINT calendar_events_kind_check CHECK (kind IN ('fixed', 'focus', 'health', 'personal')),
    CONSTRAINT calendar_events_version_check CHECK (version > 0),
    CONSTRAINT calendar_events_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX calendar_events_user_window_idx
    ON dayorder.calendar_events (user_id, start_at, end_at, id)
    WHERE deleted_at IS NULL;
CREATE INDEX calendar_events_user_goal_idx
    ON dayorder.calendar_events (user_id, goal_id)
    WHERE deleted_at IS NULL AND goal_id IS NOT NULL;

CREATE TABLE dayorder.calendar_event_reminders (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    event_id UUID NOT NULL,
    offset_minutes INTEGER NOT NULL,
    channel VARCHAR(24) NOT NULL DEFAULT 'in_app',
    scheduled_at TIMESTAMPTZ NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'pending',
    delivered_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT calendar_event_reminders_event_fk FOREIGN KEY (user_id, event_id)
        REFERENCES dayorder.calendar_events(user_id, id) ON DELETE CASCADE,
    CONSTRAINT calendar_event_reminders_offset_check CHECK (offset_minutes >= 0),
    CONSTRAINT calendar_event_reminders_channel_check CHECK (channel IN ('in_app', 'email')),
    CONSTRAINT calendar_event_reminders_status_check CHECK (
        status IN ('pending', 'processing', 'delivered', 'failed', 'cancelled')
    ),
    CONSTRAINT calendar_event_reminders_attempts_check CHECK (attempts >= 0),
    CONSTRAINT calendar_event_reminders_version_check CHECK (version > 0),
    CONSTRAINT calendar_event_reminders_user_pair_unique UNIQUE (user_id, id),
    CONSTRAINT calendar_event_reminders_event_offset_channel_unique
        UNIQUE (user_id, event_id, offset_minutes, channel)
);

CREATE INDEX calendar_event_reminders_due_idx
    ON dayorder.calendar_event_reminders (scheduled_at, id)
    WHERE deleted_at IS NULL AND status = 'pending';

CREATE TABLE dayorder.notes (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    title VARCHAR(240) NOT NULL,
    body_markdown TEXT NOT NULL DEFAULT '',
    category VARCHAR(80) NOT NULL DEFAULT '其他',
    archived_at TIMESTAMPTZ,
    search_vector TSVECTOR GENERATED ALWAYS AS (
        to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(body_markdown, ''))
    ) STORED,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT notes_title_check CHECK (length(btrim(title)) BETWEEN 1 AND 240),
    CONSTRAINT notes_category_check CHECK (length(btrim(category)) BETWEEN 1 AND 80),
    CONSTRAINT notes_version_check CHECK (version > 0),
    CONSTRAINT notes_user_pair_unique UNIQUE (user_id, id)
);

CREATE INDEX notes_user_updated_idx
    ON dayorder.notes (user_id, updated_at DESC, id)
    WHERE deleted_at IS NULL;
CREATE INDEX notes_search_vector_idx ON dayorder.notes USING GIN (search_vector);

CREATE TABLE dayorder.daily_reviews (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    review_date DATE NOT NULL,
    wins TEXT NOT NULL DEFAULT '',
    blockers TEXT NOT NULL DEFAULT '',
    mood SMALLINT,
    energy SMALLINT,
    tomorrow_focus TEXT NOT NULL DEFAULT '',
    ai_summary TEXT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT daily_reviews_mood_check CHECK (mood IS NULL OR mood BETWEEN 1 AND 5),
    CONSTRAINT daily_reviews_energy_check CHECK (energy IS NULL OR energy BETWEEN 1 AND 5),
    CONSTRAINT daily_reviews_version_check CHECK (version > 0),
    CONSTRAINT daily_reviews_user_pair_unique UNIQUE (user_id, id)
);

CREATE UNIQUE INDEX daily_reviews_user_date_active_uidx
    ON dayorder.daily_reviews (user_id, review_date)
    WHERE deleted_at IS NULL;

CREATE TABLE dayorder.tags (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    name VARCHAR(80) NOT NULL,
    normalized_name VARCHAR(80) NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT tags_name_check CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    CONSTRAINT tags_normalized_name_check CHECK (
        length(normalized_name) BETWEEN 1 AND 80 AND normalized_name = lower(btrim(normalized_name))
    ),
    CONSTRAINT tags_version_check CHECK (version > 0),
    CONSTRAINT tags_user_pair_unique UNIQUE (user_id, id)
);

CREATE UNIQUE INDEX tags_user_normalized_name_active_uidx
    ON dayorder.tags (user_id, normalized_name)
    WHERE deleted_at IS NULL;

CREATE TABLE dayorder.record_tags (
    user_id UUID NOT NULL,
    record_id UUID NOT NULL,
    tag_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, record_id, tag_id),
    CONSTRAINT record_tags_record_fk FOREIGN KEY (user_id, record_id)
        REFERENCES dayorder.records(user_id, id) ON DELETE CASCADE,
    CONSTRAINT record_tags_tag_fk FOREIGN KEY (user_id, tag_id)
        REFERENCES dayorder.tags(user_id, id) ON DELETE CASCADE
);

CREATE INDEX record_tags_tag_idx ON dayorder.record_tags (user_id, tag_id, record_id);

CREATE TABLE dayorder.note_tags (
    user_id UUID NOT NULL,
    note_id UUID NOT NULL,
    tag_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, note_id, tag_id),
    CONSTRAINT note_tags_note_fk FOREIGN KEY (user_id, note_id)
        REFERENCES dayorder.notes(user_id, id) ON DELETE CASCADE,
    CONSTRAINT note_tags_tag_fk FOREIGN KEY (user_id, tag_id)
        REFERENCES dayorder.tags(user_id, id) ON DELETE CASCADE
);

CREATE INDEX note_tags_tag_idx ON dayorder.note_tags (user_id, tag_id, note_id);

CREATE TABLE dayorder.entity_links (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES dayorder.users(id) ON DELETE CASCADE,
    source_type VARCHAR(32) NOT NULL,
    source_id UUID NOT NULL,
    target_type VARCHAR(32) NOT NULL,
    target_id UUID NOT NULL,
    relation_type VARCHAR(40) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT entity_links_source_type_check CHECK (
        source_type IN ('goal', 'milestone', 'task', 'calendar_event', 'record', 'note', 'daily_review')
    ),
    CONSTRAINT entity_links_target_type_check CHECK (
        target_type IN ('goal', 'milestone', 'task', 'calendar_event', 'record', 'note', 'daily_review')
    ),
    CONSTRAINT entity_links_relation_type_check CHECK (length(btrim(relation_type)) BETWEEN 1 AND 40),
    CONSTRAINT entity_links_not_self_check CHECK (
        source_type <> target_type OR source_id <> target_id
    ),
    CONSTRAINT entity_links_user_pair_unique UNIQUE (user_id, id),
    CONSTRAINT entity_links_relation_unique UNIQUE (
        user_id, source_type, source_id, target_type, target_id, relation_type
    )
);

CREATE INDEX entity_links_source_idx
    ON dayorder.entity_links (user_id, source_type, source_id);
CREATE INDEX entity_links_target_idx
    ON dayorder.entity_links (user_id, target_type, target_id);

COMMIT;
