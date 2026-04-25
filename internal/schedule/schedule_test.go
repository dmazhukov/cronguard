package schedule_test

import (
	"testing"
	"time"

	"github.com/dmazhukov/cronguard/internal/schedule"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every minute", "* * * * *", false},
		{"every 5 minutes", "*/5 * * * *", false},
		{"daily 2am", "0 2 * * *", false},
		{"weekly monday", "0 9 * * 1", false},
		{"descriptor", "@hourly", false},
		{"empty", "", true},
		{"garbage", "not-a-cron", true},
		{"six fields rejected", "0 0 2 * * *", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := schedule.Parse(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

func TestNext(t *testing.T) {
	s, err := schedule.Parse("0 2 * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	from := time.Date(2026, 4, 24, 1, 0, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 4, 24, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestPrev(t *testing.T) {
	s, err := schedule.Parse("0 2 * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	from := time.Date(2026, 4, 24, 3, 0, 0, 0, time.UTC)
	got := s.Prev(from)
	want := time.Date(2026, 4, 24, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Prev = %v, want %v", got, want)
	}
}

func TestDrift(t *testing.T) {
	expected := time.Date(2026, 4, 24, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		actual time.Time
		want   time.Duration
	}{
		{"exact", expected, 0},
		{"late 30s", expected.Add(30 * time.Second), 30 * time.Second},
		{"early 10s", expected.Add(-10 * time.Second), -10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schedule.Drift(tt.actual, expected); got != tt.want {
				t.Fatalf("Drift = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMissedRunsSince(t *testing.T) {
	s, err := schedule.Parse("0 * * * *") // hourly
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Last successful start at 10:00 UTC. Now is 13:30 UTC with 60s grace.
	// Expected slots within (10:00, 13:30 - 60s]: 11:00, 12:00, 13:00 => 3.
	lastStart := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 24, 13, 30, 0, 0, time.UTC)
	grace := 60 * time.Second
	got := s.MissedRunsSince(lastStart, now, grace)
	if got != 3 {
		t.Fatalf("MissedRunsSince = %d, want 3", got)
	}
}

func TestMissedRunsSinceWithinGrace(t *testing.T) {
	s, err := schedule.Parse("0 * * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 11:00 slot is within grace (now is 11:00:30, grace 60s) -> not missed.
	lastStart := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 24, 11, 0, 30, 0, time.UTC)
	grace := 60 * time.Second
	got := s.MissedRunsSince(lastStart, now, grace)
	if got != 0 {
		t.Fatalf("MissedRunsSince = %d, want 0", got)
	}
}
