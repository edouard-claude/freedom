package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Schedule defines a daily active window (start <= end, same day).
type Schedule struct {
	StartHour, StartMin int
	EndHour, EndMin     int
	Location            *time.Location
}

// Parse parses a schedule string "HH:MM-HH:MM" in the given timezone.
func Parse(raw, timezone string) (Schedule, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}

	var sh, sm, eh, em int
	n, err := fmt.Sscanf(raw, "%d:%d-%d:%d", &sh, &sm, &eh, &em)
	if err != nil || n != 4 {
		return Schedule{}, fmt.Errorf("invalid schedule format %q, expected HH:MM-HH:MM", raw)
	}
	// Verify exact format — Sscanf silently ignores trailing garbage.
	canonical := fmt.Sprintf("%02d:%02d-%02d:%02d", sh, sm, eh, em)
	if raw != canonical {
		return Schedule{}, fmt.Errorf("invalid schedule format %q, expected %s", raw, canonical)
	}

	if sh < 0 || sh > 23 || sm < 0 || sm > 59 {
		return Schedule{}, fmt.Errorf("invalid start time %02d:%02d", sh, sm)
	}
	if eh < 0 || eh > 23 || em < 0 || em > 59 {
		return Schedule{}, fmt.Errorf("invalid end time %02d:%02d", eh, em)
	}
	if sh > eh || (sh == eh && sm >= em) {
		return Schedule{}, fmt.Errorf("start time %02d:%02d must be before end time %02d:%02d", sh, sm, eh, em)
	}

	return Schedule{
		StartHour: sh, StartMin: sm,
		EndHour: eh, EndMin: em,
		Location: loc,
	}, nil
}

// IsActive reports whether the given time falls within the schedule window.
func (s Schedule) IsActive(t time.Time) bool {
	t = t.In(s.Location)
	mins := t.Hour()*60 + t.Minute()
	return mins >= s.StartHour*60+s.StartMin && mins < s.EndHour*60+s.EndMin
}

// NextStart returns the next start time at or after t.
func (s Schedule) NextStart(t time.Time) time.Time {
	t = t.In(s.Location)
	today := time.Date(t.Year(), t.Month(), t.Day(), s.StartHour, s.StartMin, 0, 0, s.Location)
	if t.Before(today) {
		return today
	}
	return today.AddDate(0, 0, 1)
}

// nextEnd returns the end time for the current or next active window relative to t.
func (s Schedule) nextEnd(t time.Time) time.Time {
	t = t.In(s.Location)
	today := time.Date(t.Year(), t.Month(), t.Day(), s.EndHour, s.EndMin, 0, 0, s.Location)
	if t.Before(today) {
		return today
	}
	return today.AddDate(0, 0, 1)
}

// WaitForWindow blocks until the schedule window is active, or ctx is cancelled.
// Returns immediately if already within the active window.
func (s Schedule) WaitForWindow(ctx context.Context, logger *slog.Logger) error {
	now := time.Now()
	if s.IsActive(now) {
		return nil
	}

	next := s.NextStart(now)
	wait := time.Until(next)
	logger.Info("outside schedule window, waiting",
		"now", now.In(s.Location).Format("15:04"),
		"next_start", next.Format("15:04"),
		"wait", wait.Round(time.Second),
	)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// ContextUntil returns a child context that is cancelled at the schedule end time.
func (s Schedule) ContextUntil(ctx context.Context, logger *slog.Logger) (context.Context, context.CancelFunc) {
	end := s.nextEnd(time.Now())
	logger.Info("schedule window active",
		"until", end.In(s.Location).Format("15:04"),
		"remaining", time.Until(end).Round(time.Second),
	)
	return context.WithDeadline(ctx, end)
}
