package postgres

import (
	"context"
	"errors"
	"fmt"

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
func (*ContentRepository) ListReviews(ctx context.Context, tx database.Tx, userID uuid.UUID, limit int) ([]model.DailyReview, error) {
	rows, err := db.New(tx).ListDailyReviews(ctx, pgUUID(userID), int32(limit))
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
