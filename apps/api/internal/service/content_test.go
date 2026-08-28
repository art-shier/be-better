package service

import (
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

func TestContentValidationNormalizesTagsAndBoundsScores(t *testing.T) {
	tags, err := normalizeTagNames([]string{" Work ", "work", "健康"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "Work" || tags[1] != "健康" {
		t.Fatalf("tags=%#v", tags)
	}
	invalid := 6
	err = validateRecord(model.Record{ID: uuid.New(), RawText: "entry", Kind: "status", OccurredAt: time.Now(), Mood: &invalid})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("record validation error=%v", err)
	}
}

func TestNormalizeLinkedEntityIDsDeduplicatesAndRejectsInvalidLinks(t *testing.T) {
	source, first, second := uuid.New(), uuid.New(), uuid.New()
	values, err := normalizeLinkedEntityIDs(source, []uuid.UUID{first, first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != first || values[1] != second {
		t.Fatalf("linked ids=%#v", values)
	}
	for _, invalid := range [][]uuid.UUID{{uuid.Nil}, {source}} {
		if _, err = normalizeLinkedEntityIDs(source, invalid); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid links %#v error=%v", invalid, err)
		}
	}
}
