package article

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestAccumulatorRun_PopulatesSegments(t *testing.T) {
	acc := NewAccumulator(4)
	segCh := make(chan Segment, 10)
	windowCh := make(chan Window, 4)

	// Send exactly 4 segments.
	for i := range 4 {
		segCh <- Segment{
			Text:   fmt.Sprintf("seg%d", i),
			RawMP3: []byte(fmt.Sprintf("audio%d", i)),
		}
	}
	close(segCh)

	err := acc.Run(context.Background(), segCh, windowCh)
	if err != nil {
		t.Fatal(err)
	}

	w, ok := <-windowCh
	if !ok {
		t.Fatal("expected a window")
	}

	// Check Segments field.
	if len(w.Segments) != 4 {
		t.Fatalf("expected 4 segments, got %d", len(w.Segments))
	}
	for i, seg := range w.Segments {
		wantText := fmt.Sprintf("seg%d", i)
		if seg.Text != wantText {
			t.Errorf("segment[%d].Text = %q, want %q", i, seg.Text, wantText)
		}
		wantMP3 := fmt.Sprintf("audio%d", i)
		if string(seg.RawMP3) != wantMP3 {
			t.Errorf("segment[%d].RawMP3 = %q, want %q", i, seg.RawMP3, wantMP3)
		}
	}

	// Check that Text and RawMP3 concatenations are still correct (non-regression).
	if !strings.Contains(w.Text, "seg0") || !strings.Contains(w.Text, "seg3") {
		t.Errorf("concatenated text missing segments: %q", w.Text)
	}

	var wantRaw []byte
	for i := range 4 {
		wantRaw = append(wantRaw, []byte(fmt.Sprintf("audio%d", i))...)
	}
	if string(w.RawMP3) != string(wantRaw) {
		t.Errorf("concatenated RawMP3 mismatch: got %q, want %q", w.RawMP3, wantRaw)
	}
}

func TestAccumulatorRun_SegmentsCopyIsDefensive(t *testing.T) {
	acc := NewAccumulator(4)
	segCh := make(chan Segment, 10)
	windowCh := make(chan Window, 4)

	// Send 8 segments — two windows: [0-3] then [2-5] (50% overlap), then [4-7].
	for i := range 8 {
		segCh <- Segment{
			Text:   fmt.Sprintf("seg%d", i),
			RawMP3: []byte(fmt.Sprintf("a%d", i)),
		}
	}
	close(segCh)

	err := acc.Run(context.Background(), segCh, windowCh)
	if err != nil {
		t.Fatal(err)
	}

	w1, ok := <-windowCh
	if !ok {
		t.Fatal("expected first window")
	}

	// First window should have segments 0-3.
	if len(w1.Segments) != 4 {
		t.Fatalf("expected 4 segments in first window, got %d", len(w1.Segments))
	}

	// Mutate w1's segments to verify the copy is independent.
	w1.Segments[2].Text = "MUTATED"
	w1.Segments[3].Text = "MUTATED"

	w2, ok := <-windowCh
	if !ok {
		t.Fatal("expected second window")
	}

	// Second window overlaps with first (segments 2-5).
	// Segments 2 and 3 should be unaffected by the mutation above.
	if w2.Segments[0].Text != "seg2" {
		t.Errorf("defensive copy failed: w2.Segments[0].Text = %q, want %q", w2.Segments[0].Text, "seg2")
	}
	if w2.Segments[1].Text != "seg3" {
		t.Errorf("defensive copy failed: w2.Segments[1].Text = %q, want %q", w2.Segments[1].Text, "seg3")
	}
}
