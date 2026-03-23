package schedule

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	s, err := Parse("06:00-20:00", "Indian/Reunion")
	if err != nil {
		t.Fatal(err)
	}
	if s.StartHour != 6 || s.StartMin != 0 || s.EndHour != 20 || s.EndMin != 0 {
		t.Fatalf("unexpected schedule: %+v", s)
	}
	if s.Location.String() != "Indian/Reunion" {
		t.Fatalf("unexpected timezone: %s", s.Location)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		tz   string
	}{
		{"bad format", "6-20", "Indian/Reunion"},
		{"bad timezone", "06:00-20:00", "Fake/Zone"},
		{"start after end", "20:00-06:00", "Indian/Reunion"},
		{"equal times", "10:00-10:00", "Indian/Reunion"},
		{"invalid hour", "25:00-20:00", "Indian/Reunion"},
		{"invalid minute", "06:60-20:00", "Indian/Reunion"},
		{"trailing garbage", "06:00-20:00xyz", "Indian/Reunion"},
		{"non-canonical", "6:0-20:0", "Indian/Reunion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw, tc.tz)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestIsActive(t *testing.T) {
	s, _ := Parse("06:00-20:00", "Indian/Reunion")

	loc := s.Location
	cases := []struct {
		hour, min int
		want      bool
	}{
		{5, 59, false},
		{6, 0, true},
		{12, 0, true},
		{19, 59, true},
		{20, 0, false},
		{23, 0, false},
	}
	for _, tc := range cases {
		t.Run(time.Date(2026, 3, 11, tc.hour, tc.min, 0, 0, loc).Format("15:04"), func(t *testing.T) {
			ts := time.Date(2026, 3, 11, tc.hour, tc.min, 0, 0, loc)
			if got := s.IsActive(ts); got != tc.want {
				t.Fatalf("isActive(%s) = %v, want %v", ts.Format("15:04"), got, tc.want)
			}
		})
	}
}

func TestNextStart(t *testing.T) {
	s, _ := Parse("06:00-20:00", "Indian/Reunion")
	loc := s.Location

	// Before start → today 06:00
	before := time.Date(2026, 3, 11, 4, 0, 0, 0, loc)
	ns := s.NextStart(before)
	if ns.Hour() != 6 || ns.Day() != 11 {
		t.Fatalf("expected today 06:00, got %v", ns)
	}

	// After start → tomorrow 06:00
	after := time.Date(2026, 3, 11, 10, 0, 0, 0, loc)
	ns = s.NextStart(after)
	if ns.Hour() != 6 || ns.Day() != 12 {
		t.Fatalf("expected tomorrow 06:00, got %v", ns)
	}
}

func TestWaitForWindowAlreadyActive(t *testing.T) {
	// Create schedule where "now" is within the window.
	s := Schedule{
		StartHour: 0, StartMin: 0,
		EndHour: 23, EndMin: 59,
	}
	loc, _ := time.LoadLocation("UTC")
	s.Location = loc

	ctx := context.Background()
	logger := slog.Default()
	if err := s.WaitForWindow(ctx, logger); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForWindowCancelledDuringActive(t *testing.T) {
	// Reproduces boot loop scenario: context cancelled while within active window.
	s := Schedule{
		StartHour: 0, StartMin: 0,
		EndHour: 23, EndMin: 59,
	}
	loc, _ := time.LoadLocation("UTC")
	s.Location = loc

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled context

	logger := slog.Default()
	err := s.WaitForWindow(ctx, logger)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestWaitForWindowCancelled(t *testing.T) {
	// Build a schedule window that is guaranteed inactive right now:
	// pick an hour range that excludes the current hour in UTC.
	loc, _ := time.LoadLocation("UTC")
	now := time.Now().In(loc)
	// Window is 2 hours before current hour (guaranteed past).
	sh := (now.Hour() + 22) % 24 // current - 2, wrapped
	eh := (now.Hour() + 23) % 24 // current - 1, wrapped
	// Skip if wrapping makes start >= end (near midnight); use a safe fallback.
	if sh >= eh {
		sh = (now.Hour() + 2) % 24
		eh = (now.Hour() + 3) % 24
		if sh >= eh {
			sh = 1
			eh = 2
		}
	}
	s := Schedule{
		StartHour: sh, StartMin: 0,
		EndHour: eh, EndMin: 0,
		Location: loc,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	logger := slog.Default()
	err := s.WaitForWindow(ctx, logger)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
