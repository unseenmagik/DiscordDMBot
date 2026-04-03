package state

import (
	"path/filepath"
	"testing"
)

func TestStoreClearMatchingFiltersByReminderAndDueDate(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))

	fileState := &FileState{
		Deliveries: map[string]DeliveryRecord{
			"reminder:payment-001:2026-04-15:id:due":   {DueDate: "2026-04-15", ReminderName: "Due Reminder"},
			"reminder:payment-001:2026-05-15:id:due":   {DueDate: "2026-05-15", ReminderName: "Due Reminder"},
			"reminder:payment-001:2026-04-15:id:final": {DueDate: "2026-04-15", ReminderName: "Final Reminder"},
			"late:payment-001:2026-04-15":              {DueDate: "2026-04-15", ReminderName: "Late Reminder"},
			"reminder:payment-002:2026-04-15:id:due":   {DueDate: "2026-04-15", ReminderName: "Due Reminder"},
		},
	}
	if err := store.Save(fileState); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	removed, err := store.ClearMatching(ClearFilter{
		DeliveryID: "payment-001",
		ReminderID: "due",
		DueDate:    "2026-04-15",
	})
	if err != nil {
		t.Fatalf("ClearMatching returned error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed entry, got %d", removed)
	}

	updatedState, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if _, exists := updatedState.Deliveries["reminder:payment-001:2026-04-15:id:due"]; exists {
		t.Fatalf("expected targeted due reminder entry to be removed")
	}
	if _, exists := updatedState.Deliveries["reminder:payment-001:2026-05-15:id:due"]; !exists {
		t.Fatalf("expected other due-date occurrence to remain")
	}
	if _, exists := updatedState.Deliveries["reminder:payment-001:2026-04-15:id:final"]; !exists {
		t.Fatalf("expected other reminder id to remain")
	}
	if _, exists := updatedState.Deliveries["late:payment-001:2026-04-15"]; !exists {
		t.Fatalf("expected late reminder state to remain")
	}
	if _, exists := updatedState.Deliveries["reminder:payment-002:2026-04-15:id:due"]; !exists {
		t.Fatalf("expected other delivery id to remain")
	}
}
