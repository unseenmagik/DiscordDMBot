package config

import (
	"testing"
	"time"
)

func TestDeliveryExpandAtSkipsManualLateReminder(t *testing.T) {
	location := time.UTC
	delivery := Delivery{
		ID:        "payment-001",
		UserID:    "123456789012345678",
		DueDate:   "2026-04-15",
		DueTime:   "17:00",
		Frequency: "once",
		Value:     "INV-001",
		Reminders: []Reminder{
			{ID: "initial", Name: "Initial Reminder", DaysBeforeDue: 3, Time: "09:00", Message: "initial"},
			{ID: "due", Name: "Due Reminder", DaysBeforeDue: 0, Time: "09:00", Message: "due"},
			{ID: "late", Name: "Late Reminder", Message: "late"},
		},
	}

	deliveries, err := delivery.ExpandAt(location, time.Date(2026, 4, 15, 12, 0, 0, 0, location))
	if err != nil {
		t.Fatalf("ExpandAt returned error: %v", err)
	}

	if len(deliveries) != 2 {
		t.Fatalf("expected 2 scheduled reminders, got %d", len(deliveries))
	}

	for _, scheduled := range deliveries {
		if scheduled.ReminderID == "late" {
			t.Fatalf("manual-only late reminder should not be expanded: %+v", scheduled)
		}
	}
}

func TestDeliveryExpandAtMonthlyAdjustsMonthEnd(t *testing.T) {
	location := time.UTC
	delivery := Delivery{
		ID:        "payment-002",
		UserID:    "123456789012345678",
		DueDate:   "2026-01-31",
		Frequency: "monthly",
		Value:     "INV-002",
		Reminders: []Reminder{
			{ID: "initial", Name: "Initial Reminder", DaysBeforeDue: 3, Time: "09:00", Message: "initial"},
			{ID: "due", Name: "Due Reminder", DaysBeforeDue: 0, Time: "09:00", Message: "due"},
		},
	}

	deliveries, err := delivery.ExpandAt(location, time.Date(2026, 2, 25, 12, 0, 0, 0, location))
	if err != nil {
		t.Fatalf("ExpandAt returned error: %v", err)
	}

	var foundAdjusted bool
	expectedReminderAt := time.Date(2026, 2, 25, 9, 0, 0, 0, location)
	for _, scheduled := range deliveries {
		if scheduled.DueDate == "2026-02-28" && scheduled.ReminderID == "initial" {
			foundAdjusted = true
			if !scheduled.ScheduledAt.Equal(expectedReminderAt) {
				t.Fatalf("expected initial reminder at %s, got %s", expectedReminderAt, scheduled.ScheduledAt)
			}
		}
	}

	if !foundAdjusted {
		t.Fatalf("expected a February month-end occurrence for 2026-02-28, got %#v", deliveries)
	}
}
