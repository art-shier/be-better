package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ContentRepository struct{}

func NewContentRepository() *ContentRepository { return &ContentRepository{} }

func (*ContentRepository) CreateRecord(ctx context.Context, tx database.Tx, userID uuid.UUID, value model.Record) (model.Record, error) {
	row, err := db.New(tx).CreateRecord(ctx, db.CreateRecordParams{ID: pgUUID(value.ID), UserID: pgUUID(userID), RawText: value.RawText, Kind: value.Kind, OccurredAt: pgTime(value.OccurredAt), Mood: pgOptionalInt2(value.Mood), Energy: pgOptionalInt2(value.Energy)})
	if err != nil {
		return model.Record{}, mapDatabaseError("create record", err)
	}
	return recordFromRow(row), nil
}
func (*ContentRepository) GetRecord(ctx context.Context, tx database.Tx, userID, id uuid.UUID) (model.Record, error) {
	row, err := db.New(tx).GetRecord(ctx, pgUUID(userID), pgUUID(id))
	if err != nil {
		return model.Record{}, mapDatabaseError("get record", err)
	}
	return recordFromRow(row), nil
}
func (*ContentRepository) ListRecords(ctx context.Context, tx database.Tx, userID uuid.UUID, after *model.ResourcePosition, limit int) ([]model.Record, error) {
	timestamp, id := pgtype.Timestamptz{}, pgtype.UUID{}
	if after != nil {
		timestamp, id = pgTime(after.UpdatedAt), pgUUID(after.ID)
	}
	rows, err := db.New(tx).ListRecords(ctx, pgUUID(userID), timestamp, id, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list records: %w", err)
	}
	values := make([]model.Record, 0, len(rows))
	for _, row := range rows {
		values = append(values, recordFromRow(row))
	}
	return values, nil
}
func (*ContentRepository) UpdateRecord(ctx context.Context, tx database.Tx, userID uuid.UUID, value model.Record, expected int64) (model.Record, error) {
	q := db.New(tx)
	row, err := q.UpdateRecord(ctx, db.UpdateRecordParams{RawText: value.RawText, Kind: value.Kind, OccurredAt: pgTime(value.OccurredAt), Mood: pgOptionalInt2(value.Mood), Energy: pgOptionalInt2(value.Energy), ArchivedAt: pgOptionalTime(value.ArchivedAt), UserID: pgUUID(userID), ID: pgUUID(value.ID), ExpectedVersion: expected})
	if err != nil {
		return model.Record{}, contentWriteError(ctx, func() error { _, e := q.GetRecord(ctx, pgUUID(userID), pgUUID(value.ID)); return e }, "update record", err)
	}
	return recordFromRow(row), nil
}
func (*ContentRepository) DeleteRecord(ctx context.Context, tx database.Tx, userID, id uuid.UUID, expected int64) (model.Record, error) {
	q := db.New(tx)
	row, err := q.SoftDeleteRecord(ctx, pgUUID(userID), pgUUID(id), expected)
	if err != nil {
		return model.Record{}, contentWriteError(ctx, func() error { _, e := q.GetRecord(ctx, pgUUID(userID), pgUUID(id)); return e }, "delete record", err)
	}
	return recordFromRow(row), nil
}

func (*ContentRepository) CreateNote(ctx context.Context, tx database.Tx, userID uuid.UUID, value model.Note) (model.Note, error) {
	row, err := db.New(tx).CreateNote(ctx, db.CreateNoteParams{ID: pgUUID(value.ID), UserID: pgUUID(userID), Title: value.Title, BodyMarkdown: value.BodyMarkdown, Category: value.Category})
	if err != nil {
		return model.Note{}, mapDatabaseError("create note", err)
	}
	return noteFromRow(row), nil
}
func (*ContentRepository) GetNote(ctx context.Context, tx database.Tx, userID, id uuid.UUID) (model.Note, error) {
	row, err := db.New(tx).GetNote(ctx, pgUUID(userID), pgUUID(id))
	if err != nil {
		return model.Note{}, mapDatabaseError("get note", err)
	}
	return noteFromRow(row), nil
}
func (*ContentRepository) ListNotes(ctx context.Context, tx database.Tx, userID uuid.UUID, after *model.ResourcePosition, limit int) ([]model.Note, error) {
	timestamp, id := pgtype.Timestamptz{}, pgtype.UUID{}
	if after != nil {
		timestamp, id = pgTime(after.UpdatedAt), pgUUID(after.ID)
	}
	rows, err := db.New(tx).ListNotes(ctx, pgUUID(userID), timestamp, id, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	return notesFromRows(rows), nil
}
func (*ContentRepository) SearchNotes(ctx context.Context, tx database.Tx, userID uuid.UUID, query string, limit int) ([]model.Note, error) {
	rows, err := db.New(tx).SearchNotes(ctx, pgUUID(userID), query, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("search notes: %w", err)
	}
	return notesFromRows(rows), nil
}
func (*ContentRepository) UpdateNote(ctx context.Context, tx database.Tx, userID uuid.UUID, value model.Note, expected int64) (model.Note, error) {
	q := db.New(tx)
	row, err := q.UpdateNote(ctx, db.UpdateNoteParams{Title: value.Title, BodyMarkdown: value.BodyMarkdown, Category: value.Category, ArchivedAt: pgOptionalTime(value.ArchivedAt), UserID: pgUUID(userID), ID: pgUUID(value.ID), ExpectedVersion: expected})
	if err != nil {
		return model.Note{}, contentWriteError(ctx, func() error { _, e := q.GetNote(ctx, pgUUID(userID), pgUUID(value.ID)); return e }, "update note", err)
	}
	return noteFromRow(row), nil
}
func (*ContentRepository) DeleteNote(ctx context.Context, tx database.Tx, userID, id uuid.UUID, expected int64) (model.Note, error) {
	q := db.New(tx)
	row, err := q.SoftDeleteNote(ctx, pgUUID(userID), pgUUID(id), expected)
	if err != nil {
		return model.Note{}, contentWriteError(ctx, func() error { _, e := q.GetNote(ctx, pgUUID(userID), pgUUID(id)); return e }, "delete note", err)
	}
	return noteFromRow(row), nil
}

func (*ContentRepository) CreateReview(ctx context.Context, tx database.Tx, userID uuid.UUID, value model.DailyReview) (model.DailyReview, error) {
	row, err := db.New(tx).CreateDailyReview(ctx, db.CreateDailyReviewParams{ID: pgUUID(value.ID), UserID: pgUUID(userID), ReviewDate: pgDate(value.ReviewDate), Wins: value.Wins, Blockers: value.Blockers, Mood: pgOptionalInt2(value.Mood), Energy: pgOptionalInt2(value.Energy), TomorrowFocus: value.TomorrowFocus, AiSummary: pgOptionalText(value.AISummary)})
	if err != nil {
		return model.DailyReview{}, mapDatabaseError("create daily review", err)
	}
	return reviewFromRow(row), nil
}
func (*ContentRepository) GetReview(ctx context.Context, tx database.Tx, userID, id uuid.UUID) (model.DailyReview, error) {
	row, err := db.New(tx).GetDailyReview(ctx, pgUUID(userID), pgUUID(id))
	if err != nil {
		return model.DailyReview{}, mapDatabaseError("get daily review", err)
	}
	return reviewFromRow(row), nil
}
func (*ContentRepository) ListReviews(ctx context.Context, tx database.Tx, userID uuid.UUID, after *model.ResourcePosition, limit int) ([]model.DailyReview, error) {
	var afterDate *string
	var afterID *uuid.UUID
	if after != nil {
		formatted := after.UpdatedAt.UTC().Format(time.DateOnly)
		afterDate, afterID = &formatted, &after.ID
	}
	rows, err := db.New(tx).ListDailyReviews(ctx, pgUUID(userID), pgOptionalDate(afterDate), pgOptionalUUID(afterID), int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list daily reviews: %w", err)
	}
	values := make([]model.DailyReview, 0, len(rows))
	for _, row := range rows {
		values = append(values, reviewFromRow(row))
	}
	return values, nil
}
func (*ContentRepository) UpdateReview(ctx context.Context, tx database.Tx, userID uuid.UUID, value model.DailyReview, expected int64) (model.DailyReview, error) {
	q := db.New(tx)
	row, err := q.UpdateDailyReview(ctx, db.UpdateDailyReviewParams{ReviewDate: pgDate(value.ReviewDate), Wins: value.Wins, Blockers: value.Blockers, Mood: pgOptionalInt2(value.Mood), Energy: pgOptionalInt2(value.Energy), TomorrowFocus: value.TomorrowFocus, AiSummary: pgOptionalText(value.AISummary), UserID: pgUUID(userID), ID: pgUUID(value.ID), ExpectedVersion: expected})
	if err != nil {
		return model.DailyReview{}, contentWriteError(ctx, func() error { _, e := q.GetDailyReview(ctx, pgUUID(userID), pgUUID(value.ID)); return e }, "update daily review", err)
	}
	return reviewFromRow(row), nil
}
func (*ContentRepository) DeleteReview(ctx context.Context, tx database.Tx, userID, id uuid.UUID, expected int64) (model.DailyReview, error) {
	q := db.New(tx)
	row, err := q.SoftDeleteDailyReview(ctx, pgUUID(userID), pgUUID(id), expected)
	if err != nil {
		return model.DailyReview{}, contentWriteError(ctx, func() error { _, e := q.GetDailyReview(ctx, pgUUID(userID), pgUUID(id)); return e }, "delete daily review", err)
	}
	return reviewFromRow(row), nil
}

func (*ContentRepository) EnsureTag(ctx context.Context, tx database.Tx, userID, id uuid.UUID, name, normalized string) (model.Tag, bool, error) {
	q := db.New(tx)
	row, err := q.GetTagByNormalizedName(ctx, pgUUID(userID), normalized)
	if err == nil {
		return tagFromRow(row), false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.Tag{}, false, fmt.Errorf("find tag: %w", err)
	}
	row, err = q.CreateTag(ctx, pgUUID(id), pgUUID(userID), name, normalized)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = q.GetTagByNormalizedName(ctx, pgUUID(userID), normalized)
	}
	if err != nil {
		return model.Tag{}, false, mapDatabaseError("create tag", err)
	}
	return tagFromRow(row), row.ID.Bytes == id, nil
}
func (*ContentRepository) ListTags(ctx context.Context, tx database.Tx, userID uuid.UUID, limit int) ([]model.Tag, error) {
	rows, err := db.New(tx).ListTags(ctx, pgUUID(userID), int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return tagsFromRows(rows), nil
}
func (*ContentRepository) ReplaceRecordTags(ctx context.Context, tx database.Tx, userID, recordID uuid.UUID, tags []model.Tag) error {
	q := db.New(tx)
	if err := q.ReplaceRecordTagsDelete(ctx, pgUUID(userID), pgUUID(recordID)); err != nil {
		return err
	}
	for _, tag := range tags {
		if err := q.LinkRecordTag(ctx, pgUUID(userID), pgUUID(recordID), pgUUID(tag.ID)); err != nil {
			return err
		}
	}
	return nil
}
func (*ContentRepository) ReplaceNoteTags(ctx context.Context, tx database.Tx, userID, noteID uuid.UUID, tags []model.Tag) error {
	q := db.New(tx)
	if err := q.ReplaceNoteTagsDelete(ctx, pgUUID(userID), pgUUID(noteID)); err != nil {
		return err
	}
	for _, tag := range tags {
		if err := q.LinkNoteTag(ctx, pgUUID(userID), pgUUID(noteID), pgUUID(tag.ID)); err != nil {
			return err
		}
	}
	return nil
}
func (*ContentRepository) ListRecordTags(ctx context.Context, tx database.Tx, userID, id uuid.UUID) ([]model.Tag, error) {
	rows, err := db.New(tx).ListRecordTags(ctx, pgUUID(userID), pgUUID(id))
	if err != nil {
		return nil, err
	}
	return tagsFromRows(rows), nil
}
func (*ContentRepository) ListNoteTags(ctx context.Context, tx database.Tx, userID, id uuid.UUID) ([]model.Tag, error) {
	rows, err := db.New(tx).ListNoteTags(ctx, pgUUID(userID), pgUUID(id))
	if err != nil {
		return nil, err
	}
	return tagsFromRows(rows), nil
}
func (*ContentRepository) ResolveEntityType(ctx context.Context, tx database.Tx, userID, id uuid.UUID) (string, error) {
	q := db.New(tx)
	lookups := []struct {
		entityType string
		get        func() error
	}{
		{entityType: "goal", get: func() error { _, err := q.GetGoal(ctx, pgUUID(userID), pgUUID(id)); return err }},
		{entityType: "milestone", get: func() error { _, err := q.GetGoalMilestone(ctx, pgUUID(userID), pgUUID(id)); return err }},
		{entityType: "task", get: func() error { _, err := q.GetTask(ctx, pgUUID(userID), pgUUID(id)); return err }},
		{entityType: "calendar_event", get: func() error { _, err := q.GetCalendarEvent(ctx, pgUUID(userID), pgUUID(id)); return err }},
		{entityType: "record", get: func() error { _, err := q.GetRecord(ctx, pgUUID(userID), pgUUID(id)); return err }},
		{entityType: "note", get: func() error { _, err := q.GetNote(ctx, pgUUID(userID), pgUUID(id)); return err }},
		{entityType: "daily_review", get: func() error { _, err := q.GetDailyReview(ctx, pgUUID(userID), pgUUID(id)); return err }},
	}
	for _, lookup := range lookups {
		err := lookup.get()
		if err == nil {
			return lookup.entityType, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("resolve linked entity: %w", err)
		}
	}
	return "", model.ErrNotFound
}
func (*ContentRepository) ReplaceNoteLinks(ctx context.Context, tx database.Tx, userID, noteID uuid.UUID, links []model.EntityLink) error {
	q := db.New(tx)
	existing, err := q.ListEntityLinks(ctx, pgUUID(userID), "note", pgUUID(noteID))
	if err != nil {
		return fmt.Errorf("list note links: %w", err)
	}
	type relationKey struct {
		targetType, targetID, relationType string
	}
	desired := make(map[relationKey]model.EntityLink, len(links))
	for _, link := range links {
		desired[relationKey{targetType: link.TargetType, targetID: link.TargetID.String(), relationType: link.RelationType}] = link
	}
	for _, row := range existing {
		key := relationKey{targetType: row.TargetType, targetID: uuid.UUID(row.TargetID.Bytes).String(), relationType: row.RelationType}
		if _, keep := desired[key]; keep {
			delete(desired, key)
			continue
		}
		if _, err = q.DeleteEntityLink(ctx, pgUUID(userID), row.ID); err != nil {
			return fmt.Errorf("delete note link: %w", err)
		}
	}
	for _, link := range desired {
		_, err = q.CreateEntityLink(ctx, db.CreateEntityLinkParams{
			ID: pgUUID(link.ID), UserID: pgUUID(userID), SourceType: "note", SourceID: pgUUID(noteID),
			TargetType: link.TargetType, TargetID: pgUUID(link.TargetID), RelationType: link.RelationType,
		})
		if err != nil {
			return mapDatabaseError("create note link", err)
		}
	}
	return nil
}
func (*ContentRepository) ListNoteLinks(ctx context.Context, tx database.Tx, userID, noteID uuid.UUID) ([]model.EntityLink, error) {
	rows, err := db.New(tx).ListEntityLinks(ctx, pgUUID(userID), "note", pgUUID(noteID))
	if err != nil {
		return nil, fmt.Errorf("list note links: %w", err)
	}
	values := make([]model.EntityLink, 0, len(rows))
	for _, row := range rows {
		values = append(values, model.EntityLink{
			ID: uuid.UUID(row.ID.Bytes), SourceType: row.SourceType, SourceID: uuid.UUID(row.SourceID.Bytes),
			TargetType: row.TargetType, TargetID: uuid.UUID(row.TargetID.Bytes), RelationType: row.RelationType, CreatedAt: row.CreatedAt.Time.UTC(),
		})
	}
	return values, nil
}
func (*ContentRepository) CleanupTags(ctx context.Context, tx database.Tx, userID uuid.UUID) ([]model.Tag, error) {
	rows, err := db.New(tx).SoftDeleteUnusedTags(ctx, pgUUID(userID))
	if err != nil {
		return nil, err
	}
	return tagsFromRows(rows), nil
}

func contentWriteError(_ context.Context, get func() error, operation string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError(operation, err)
	}
	if readErr := get(); errors.Is(readErr, pgx.ErrNoRows) {
		return model.ErrNotFound
	} else if readErr != nil {
		return fmt.Errorf("check resource after failed write: %w", readErr)
	}
	return model.ErrConflict
}
func recordFromRow(row *db.DayorderRecord) model.Record {
	return model.Record{ID: uuid.UUID(row.ID.Bytes), RawText: row.RawText, Kind: row.Kind, OccurredAt: row.OccurredAt.Time.UTC(), Mood: optionalInt2(row.Mood), Energy: optionalInt2(row.Energy), ArchivedAt: optionalTime(row.ArchivedAt), Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt)}
}
func noteFromRow(row *db.DayorderNote) model.Note {
	return model.Note{ID: uuid.UUID(row.ID.Bytes), Title: row.Title, BodyMarkdown: row.BodyMarkdown, Category: row.Category, ArchivedAt: optionalTime(row.ArchivedAt), Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt)}
}
func notesFromRows(rows []*db.DayorderNote) []model.Note {
	values := make([]model.Note, 0, len(rows))
	for _, row := range rows {
		values = append(values, noteFromRow(row))
	}
	return values
}
func reviewFromRow(row *db.DayorderDailyReview) model.DailyReview {
	return model.DailyReview{ID: uuid.UUID(row.ID.Bytes), ReviewDate: dateString(row.ReviewDate), Wins: row.Wins, Blockers: row.Blockers, Mood: optionalInt2(row.Mood), Energy: optionalInt2(row.Energy), TomorrowFocus: row.TomorrowFocus, AISummary: optionalText(row.AiSummary), Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt)}
}
func tagFromRow(row *db.DayorderTag) model.Tag {
	return model.Tag{ID: uuid.UUID(row.ID.Bytes), Name: row.Name, Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt)}
}
func tagsFromRows(rows []*db.DayorderTag) []model.Tag {
	values := make([]model.Tag, 0, len(rows))
	for _, row := range rows {
		values = append(values, tagFromRow(row))
	}
	return values
}
