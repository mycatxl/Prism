package main

import (
	"errors"
	"testing"

	"prism/internal/model"
)

type fixtureAlertJournal struct {
	failure   error
	row       model.UsageReservation
	settledID string
	actual    int64
}

func (s *fixtureAlertJournal) ReserveUsage(row model.UsageReservation) error {
	s.row = row
	return s.failure
}
func (s *fixtureAlertJournal) SettleUsage(id string, actual int64) (bool, error) {
	s.settledID, s.actual = id, actual
	return false, s.failure
}
func (s *fixtureAlertJournal) PendingUsage() ([]model.UsageReservation, error) {
	return []model.UsageReservation{s.row}, s.failure
}

func TestAlertJournalObservesFailuresWithoutChangingDurableAccounting(t *testing.T) {
	failure := errors.New("fixture database lock")
	inner := &fixtureAlertJournal{failure: failure}
	var reports int
	store := alertJournalStore{JournalStore: inner, report: func(err error) {
		if !errors.Is(err, failure) {
			t.Error("observer changed the error")
		}
		reports++
	}}
	row := model.UsageReservation{ID: "fixture-reservation", UserID: "fixture-user", Bytes: 100}
	if err := store.ReserveUsage(row); !errors.Is(err, failure) || inner.row != row {
		t.Fatal("reservation was changed by notification wiring")
	}
	if settled, err := store.SettleUsage(row.ID, 73); settled || !errors.Is(err, failure) || inner.actual != 73 || inner.settledID != row.ID {
		t.Fatal("settlement identity or accepted-byte count changed")
	}
	if rows, err := store.PendingUsage(); !errors.Is(err, failure) || len(rows) != 1 || rows[0] != row || reports != 3 {
		t.Fatal("pending liability or failure notification was lost")
	}
	inner.failure = nil
	if err := store.ReserveUsage(row); err != nil || reports != 3 {
		t.Fatal("successful accounting generated a fault notification")
	}
}
