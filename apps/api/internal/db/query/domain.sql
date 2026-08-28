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
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(page_size);

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

-- name: CreateCalendarEvent :one
INSERT INTO dayorder.calendar_events (
    id, user_id, title, start_at, end_at, timezone, location, kind, source_calendar, goal_id
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(title), sqlc.arg(start_at), sqlc.arg(end_at),
    sqlc.arg(timezone), sqlc.narg(location), sqlc.arg(kind), sqlc.narg(source_calendar),
    sqlc.narg(goal_id)
)
RETURNING *;

-- name: CreateNote :one
INSERT INTO dayorder.notes (
    id, user_id, title, body_markdown, category
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(title), sqlc.arg(body_markdown), sqlc.arg(category)
)
RETURNING *;

-- name: SearchNotes :many
SELECT *
FROM dayorder.notes
WHERE user_id = sqlc.arg(user_id)
  AND deleted_at IS NULL
  AND search_vector @@ websearch_to_tsquery('simple', sqlc.arg(query))
ORDER BY ts_rank(search_vector, websearch_to_tsquery('simple', sqlc.arg(query))) DESC, updated_at DESC
LIMIT sqlc.arg(page_size);
