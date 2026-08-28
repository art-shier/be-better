-- name: CreateGoal :one
INSERT INTO dayorder.goals (
    id, user_id, title, why, area, metric_type, target_value, current_value,
    unit, start_date, due_date, status, health
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(title), sqlc.arg(why), sqlc.arg(area),
    sqlc.arg(metric_type), sqlc.arg(target_value), sqlc.arg(current_value), sqlc.arg(unit),
    sqlc.arg(start_date), sqlc.narg(due_date), sqlc.arg(status), sqlc.arg(health)
)
RETURNING *;

-- name: GetGoal :one
SELECT *
FROM dayorder.goals
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: ListGoals :many
SELECT *
FROM dayorder.goals
WHERE user_id = sqlc.arg(user_id)
  AND deleted_at IS NULL
  AND (
      sqlc.narg(after_updated_at)::timestamptz IS NULL
      OR updated_at < sqlc.narg(after_updated_at)::timestamptz
      OR (
          updated_at = sqlc.narg(after_updated_at)::timestamptz
          AND id < sqlc.narg(after_id)::uuid
      )
  )
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: UpdateGoalProgress :one
UPDATE dayorder.goals
SET current_value = sqlc.arg(current_value),
    health = sqlc.arg(health),
    version = version + 1,
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: UpdateGoal :one
UPDATE dayorder.goals
SET title = sqlc.arg(title),
    why = sqlc.arg(why),
    area = sqlc.arg(area),
    metric_type = sqlc.arg(metric_type),
    target_value = sqlc.arg(target_value),
    current_value = sqlc.arg(current_value),
    unit = sqlc.arg(unit),
    start_date = sqlc.arg(start_date),
    due_date = sqlc.narg(due_date),
    status = sqlc.arg(status),
    health = sqlc.arg(health),
    version = version + 1,
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteGoal :one
UPDATE dayorder.goals
SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: CreateGoalMilestone :one
INSERT INTO dayorder.goal_milestones (
    id, user_id, goal_id, title, due_at, completed_at, sort_order
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(goal_id), sqlc.arg(title),
    sqlc.narg(due_at), sqlc.narg(completed_at), sqlc.arg(sort_order)
)
RETURNING *;

-- name: GetGoalMilestone :one
SELECT *
FROM dayorder.goal_milestones
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: ListGoalMilestones :many
SELECT *
FROM dayorder.goal_milestones
WHERE user_id = sqlc.arg(user_id)
  AND goal_id = sqlc.arg(goal_id)
  AND deleted_at IS NULL
ORDER BY sort_order, created_at, id;

-- name: UpdateGoalMilestone :one
UPDATE dayorder.goal_milestones
SET title = sqlc.arg(title),
    due_at = sqlc.narg(due_at),
    completed_at = sqlc.narg(completed_at),
    sort_order = sqlc.arg(sort_order),
    version = version + 1,
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteGoalMilestone :one
UPDATE dayorder.goal_milestones
SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteGoalMilestones :many
UPDATE dayorder.goal_milestones
SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND goal_id = sqlc.arg(goal_id)
  AND deleted_at IS NULL
RETURNING *;

-- name: DetachGoalTasks :many
UPDATE dayorder.tasks
SET goal_id = NULL, version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND goal_id = sqlc.arg(goal_id)
  AND deleted_at IS NULL
RETURNING *;

-- name: CreateTask :one
INSERT INTO dayorder.tasks (
    id, user_id, title, status, priority, estimate_minutes, due_at,
    scheduled_start, scheduled_end, goal_id, source_record_id, completed_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(title), sqlc.arg(status), sqlc.arg(priority),
    sqlc.arg(estimate_minutes), sqlc.narg(due_at), sqlc.narg(scheduled_start),
    sqlc.narg(scheduled_end), sqlc.narg(goal_id), sqlc.narg(source_record_id),
    sqlc.narg(completed_at)
)
RETURNING *;

-- name: GetTask :one
SELECT *
FROM dayorder.tasks
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: ListTasks :many
SELECT *
FROM dayorder.tasks
WHERE user_id = sqlc.arg(user_id)
  AND deleted_at IS NULL
  AND (sqlc.narg(status)::varchar IS NULL OR status = sqlc.narg(status))
  AND (
      sqlc.narg(after_updated_at)::timestamptz IS NULL
      OR updated_at < sqlc.narg(after_updated_at)::timestamptz
      OR (
          updated_at = sqlc.narg(after_updated_at)::timestamptz
          AND id < sqlc.narg(after_id)::uuid
      )
  )
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: UpdateTask :one
UPDATE dayorder.tasks
SET title = sqlc.arg(title),
    status = sqlc.arg(status),
    priority = sqlc.arg(priority),
    estimate_minutes = sqlc.arg(estimate_minutes),
    due_at = sqlc.narg(due_at),
    scheduled_start = sqlc.narg(scheduled_start),
    scheduled_end = sqlc.narg(scheduled_end),
    goal_id = sqlc.narg(goal_id),
    source_record_id = sqlc.narg(source_record_id),
    completed_at = sqlc.narg(completed_at),
    version = version + 1,
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteTask :one
UPDATE dayorder.tasks
SET deleted_at = now(),
    version = version + 1,
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
RETURNING *;

-- name: CreateRecord :one
INSERT INTO dayorder.records (
    id, user_id, raw_text, kind, occurred_at, mood, energy
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(raw_text), sqlc.arg(kind),
    sqlc.arg(occurred_at), sqlc.narg(mood), sqlc.narg(energy)
)
RETURNING *;

-- name: GetRecord :one
SELECT * FROM dayorder.records
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListRecords :many
SELECT * FROM dayorder.records
WHERE user_id = sqlc.arg(user_id) AND deleted_at IS NULL
  AND (sqlc.narg(after_occurred_at)::timestamptz IS NULL OR occurred_at < sqlc.narg(after_occurred_at)
       OR (occurred_at = sqlc.narg(after_occurred_at) AND id < sqlc.narg(after_id)::uuid))
ORDER BY occurred_at DESC, id DESC LIMIT sqlc.arg(page_size);

-- name: UpdateRecord :one
UPDATE dayorder.records
SET raw_text = sqlc.arg(raw_text), kind = sqlc.arg(kind), occurred_at = sqlc.arg(occurred_at),
    mood = sqlc.narg(mood), energy = sqlc.narg(energy), archived_at = sqlc.narg(archived_at),
    version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteRecord :one
UPDATE dayorder.records SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL RETURNING *;

-- name: CreateCalendarEvent :one
INSERT INTO dayorder.calendar_events (
    id, user_id, title, start_at, end_at, timezone, location, kind, source_calendar, goal_id
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(title), sqlc.arg(start_at), sqlc.arg(end_at),
    sqlc.arg(timezone), sqlc.narg(location), sqlc.arg(kind), sqlc.narg(source_calendar),
    sqlc.narg(goal_id)
)
RETURNING *;

-- name: GetCalendarEvent :one
SELECT * FROM dayorder.calendar_events
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListCalendarEvents :many
SELECT * FROM dayorder.calendar_events
WHERE user_id = sqlc.arg(user_id)
  AND deleted_at IS NULL
  AND (sqlc.narg(window_start)::timestamptz IS NULL OR end_at >= sqlc.narg(window_start))
  AND (sqlc.narg(window_end)::timestamptz IS NULL OR start_at <= sqlc.narg(window_end))
  AND (
      sqlc.narg(after_start_at)::timestamptz IS NULL
      OR start_at > sqlc.narg(after_start_at)::timestamptz
      OR (start_at = sqlc.narg(after_start_at)::timestamptz AND id > sqlc.narg(after_id)::uuid)
  )
ORDER BY start_at, id
LIMIT sqlc.arg(page_size);

-- name: UpdateCalendarEvent :one
UPDATE dayorder.calendar_events
SET title = sqlc.arg(title), start_at = sqlc.arg(start_at), end_at = sqlc.arg(end_at),
    timezone = sqlc.arg(timezone), location = sqlc.narg(location), kind = sqlc.arg(kind),
    source_calendar = sqlc.narg(source_calendar), goal_id = sqlc.narg(goal_id),
    version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteCalendarEvent :one
UPDATE dayorder.calendar_events
SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL
RETURNING *;

-- name: CreateCalendarReminder :one
INSERT INTO dayorder.calendar_event_reminders (
    id, user_id, event_id, offset_minutes, channel, scheduled_at
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(event_id), sqlc.arg(offset_minutes),
    sqlc.arg(channel), sqlc.arg(scheduled_at)
)
RETURNING *;

-- name: ListCalendarReminders :many
SELECT * FROM dayorder.calendar_event_reminders
WHERE user_id = sqlc.arg(user_id) AND event_id = sqlc.arg(event_id) AND deleted_at IS NULL
ORDER BY offset_minutes DESC, channel, id;

-- name: GetCalendarReminder :one
SELECT * FROM dayorder.calendar_event_reminders
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: RescheduleCalendarReminders :many
UPDATE dayorder.calendar_event_reminders
SET scheduled_at = sqlc.arg(start_at)::timestamptz - (offset_minutes * interval '1 minute'),
    status = 'pending', delivered_at = NULL, attempts = 0,
    version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND event_id = sqlc.arg(event_id) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteCalendarReminders :many
UPDATE dayorder.calendar_event_reminders
SET deleted_at = now(), status = 'cancelled', version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND event_id = sqlc.arg(event_id) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteCalendarReminder :one
UPDATE dayorder.calendar_event_reminders
SET deleted_at = now(), status = 'cancelled', version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND event_id = sqlc.arg(event_id)
  AND id = sqlc.arg(reminder_id) AND deleted_at IS NULL
RETURNING *;

-- name: GetReminderDelivery :one
SELECT
    reminder.user_id,
    reminder.id AS reminder_id,
    reminder.event_id,
    reminder.channel,
    reminder.scheduled_at,
    reminder.status,
    reminder.version AS reminder_version,
    reminder.deleted_at AS reminder_deleted_at,
    account.status AS account_status,
    account.deleted_at AS account_deleted_at,
    account.email,
    account.display_name,
    event.title AS event_title,
    event.start_at AS event_start_at,
    event.timezone,
    event.deleted_at AS event_deleted_at
FROM dayorder.calendar_event_reminders AS reminder
JOIN dayorder.calendar_events AS event
  ON event.user_id = reminder.user_id AND event.id = reminder.event_id
JOIN dayorder.users AS account
  ON account.id = reminder.user_id
WHERE reminder.user_id = sqlc.arg(user_id)
  AND reminder.id = sqlc.arg(reminder_id);

-- name: GetReminderDeliveryForUpdate :one
SELECT
    reminder.user_id,
    reminder.id AS reminder_id,
    reminder.event_id,
    reminder.channel,
    reminder.scheduled_at,
    reminder.status,
    reminder.version AS reminder_version,
    reminder.deleted_at AS reminder_deleted_at,
    account.status AS account_status,
    account.deleted_at AS account_deleted_at,
    account.email,
    account.display_name,
    event.title AS event_title,
    event.start_at AS event_start_at,
    event.timezone,
    event.deleted_at AS event_deleted_at
FROM dayorder.calendar_event_reminders AS reminder
JOIN dayorder.calendar_events AS event
  ON event.user_id = reminder.user_id AND event.id = reminder.event_id
JOIN dayorder.users AS account
  ON account.id = reminder.user_id
WHERE reminder.user_id = sqlc.arg(user_id)
  AND reminder.id = sqlc.arg(reminder_id)
FOR UPDATE OF reminder;

-- name: RecordReminderDeliveryResult :one
UPDATE dayorder.calendar_event_reminders
SET status = sqlc.arg(status),
    delivered_at = CASE WHEN sqlc.arg(status)::varchar = 'delivered' THEN now() ELSE NULL END,
    attempts = attempts + 1,
    version = version + 1,
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(reminder_id)
  AND event_id = sqlc.arg(event_id)
  AND channel = sqlc.arg(channel)
  AND scheduled_at = sqlc.arg(scheduled_at)
  AND version = sqlc.arg(expected_version)
  AND deleted_at IS NULL
  AND status IN ('pending', 'processing', 'failed')
RETURNING *;

-- name: CreateNote :one
INSERT INTO dayorder.notes (
    id, user_id, title, body_markdown, category
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(title), sqlc.arg(body_markdown), sqlc.arg(category)
)
RETURNING *;

-- name: GetNote :one
SELECT * FROM dayorder.notes
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListNotes :many
SELECT * FROM dayorder.notes
WHERE user_id = sqlc.arg(user_id) AND deleted_at IS NULL
  AND (sqlc.narg(after_updated_at)::timestamptz IS NULL OR updated_at < sqlc.narg(after_updated_at)
       OR (updated_at = sqlc.narg(after_updated_at) AND id < sqlc.narg(after_id)::uuid))
ORDER BY updated_at DESC, id DESC LIMIT sqlc.arg(page_size);

-- name: UpdateNote :one
UPDATE dayorder.notes
SET title = sqlc.arg(title), body_markdown = sqlc.arg(body_markdown), category = sqlc.arg(category),
    archived_at = sqlc.narg(archived_at), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL RETURNING *;

-- name: SoftDeleteNote :one
UPDATE dayorder.notes SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL RETURNING *;

-- name: SearchNotes :many
SELECT *
FROM dayorder.notes
WHERE user_id = sqlc.arg(user_id)
  AND deleted_at IS NULL
  AND search_vector @@ websearch_to_tsquery('simple', sqlc.arg(query))
ORDER BY ts_rank(search_vector, websearch_to_tsquery('simple', sqlc.arg(query))) DESC, updated_at DESC
LIMIT sqlc.arg(page_size);

-- name: CreateDailyReview :one
INSERT INTO dayorder.daily_reviews (
    id, user_id, review_date, wins, blockers, mood, energy, tomorrow_focus, ai_summary
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(review_date), sqlc.arg(wins), sqlc.arg(blockers),
    sqlc.narg(mood), sqlc.narg(energy), sqlc.arg(tomorrow_focus), sqlc.narg(ai_summary)
) RETURNING *;

-- name: GetDailyReview :one
SELECT * FROM dayorder.daily_reviews
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListDailyReviews :many
SELECT * FROM dayorder.daily_reviews
WHERE user_id = sqlc.arg(user_id) AND deleted_at IS NULL
  AND (
      sqlc.narg(after_review_date)::date IS NULL
      OR review_date < sqlc.narg(after_review_date)::date
      OR (review_date = sqlc.narg(after_review_date)::date AND id < sqlc.narg(after_id)::uuid)
  )
ORDER BY review_date DESC, id DESC LIMIT sqlc.arg(page_size);

-- name: UpdateDailyReview :one
UPDATE dayorder.daily_reviews
SET review_date = sqlc.arg(review_date), wins = sqlc.arg(wins), blockers = sqlc.arg(blockers),
    mood = sqlc.narg(mood), energy = sqlc.narg(energy), tomorrow_focus = sqlc.arg(tomorrow_focus),
    ai_summary = sqlc.narg(ai_summary), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL RETURNING *;

-- name: SoftDeleteDailyReview :one
UPDATE dayorder.daily_reviews SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version) AND deleted_at IS NULL RETURNING *;

-- name: CreateTag :one
INSERT INTO dayorder.tags (id, user_id, name, normalized_name)
VALUES (sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(normalized_name))
ON CONFLICT (user_id, normalized_name) WHERE deleted_at IS NULL DO NOTHING
RETURNING *;

-- name: GetTagByNormalizedName :one
SELECT * FROM dayorder.tags
WHERE user_id = sqlc.arg(user_id) AND normalized_name = sqlc.arg(normalized_name) AND deleted_at IS NULL;

-- name: GetTag :one
SELECT * FROM dayorder.tags
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListTags :many
SELECT * FROM dayorder.tags WHERE user_id = sqlc.arg(user_id) AND deleted_at IS NULL
ORDER BY normalized_name, id LIMIT sqlc.arg(page_size);

-- name: LinkRecordTag :exec
INSERT INTO dayorder.record_tags (user_id, record_id, tag_id)
VALUES (sqlc.arg(user_id), sqlc.arg(record_id), sqlc.arg(tag_id)) ON CONFLICT DO NOTHING;

-- name: LinkNoteTag :exec
INSERT INTO dayorder.note_tags (user_id, note_id, tag_id)
VALUES (sqlc.arg(user_id), sqlc.arg(note_id), sqlc.arg(tag_id)) ON CONFLICT DO NOTHING;

-- name: ListRecordTags :many
SELECT tags.* FROM dayorder.tags
JOIN dayorder.record_tags links ON links.user_id = tags.user_id AND links.tag_id = tags.id
WHERE links.user_id = sqlc.arg(user_id) AND links.record_id = sqlc.arg(record_id) AND tags.deleted_at IS NULL
ORDER BY tags.normalized_name;

-- name: ListNoteTags :many
SELECT tags.* FROM dayorder.tags
JOIN dayorder.note_tags links ON links.user_id = tags.user_id AND links.tag_id = tags.id
WHERE links.user_id = sqlc.arg(user_id) AND links.note_id = sqlc.arg(note_id) AND tags.deleted_at IS NULL
ORDER BY tags.normalized_name;

-- name: ReplaceRecordTagsDelete :exec
DELETE FROM dayorder.record_tags WHERE user_id = sqlc.arg(user_id) AND record_id = sqlc.arg(record_id);

-- name: ReplaceNoteTagsDelete :exec
DELETE FROM dayorder.note_tags WHERE user_id = sqlc.arg(user_id) AND note_id = sqlc.arg(note_id);

-- name: SoftDeleteUnusedTags :many
UPDATE dayorder.tags tag SET deleted_at = now(), version = version + 1, updated_at = now()
WHERE tag.user_id = sqlc.arg(user_id) AND tag.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM dayorder.record_tags rt WHERE rt.user_id = tag.user_id AND rt.tag_id = tag.id)
  AND NOT EXISTS (SELECT 1 FROM dayorder.note_tags nt WHERE nt.user_id = tag.user_id AND nt.tag_id = tag.id)
RETURNING tag.*;

-- name: CreateEntityLink :one
INSERT INTO dayorder.entity_links (
    id, user_id, source_type, source_id, target_type, target_id, relation_type
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(source_type), sqlc.arg(source_id),
    sqlc.arg(target_type), sqlc.arg(target_id), sqlc.arg(relation_type)
) RETURNING *;

-- name: ListEntityLinks :many
SELECT * FROM dayorder.entity_links
WHERE user_id = sqlc.arg(user_id) AND source_type = sqlc.arg(source_type) AND source_id = sqlc.arg(source_id)
ORDER BY created_at, id;

-- name: DeleteEntityLink :execrows
DELETE FROM dayorder.entity_links WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);
