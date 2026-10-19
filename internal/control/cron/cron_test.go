package cron

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone %s unavailable: %v", name, err)
	}
	return loc
}

func at(loc *time.Location, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	utc := time.UTC
	tests := []struct {
		spec, after, want string
	}{
		{"* * * * *", "2026-10-17 10:00", "2026-10-17 10:01"},
		{"*/15 * * * *", "2026-10-17 10:07", "2026-10-17 10:15"},
		{"*/15 * * * *", "2026-10-17 10:45", "2026-10-17 11:00"},
		{"0 3 * * *", "2026-10-17 03:00", "2026-10-18 03:00"},
		{"0 3 * * *", "2026-10-17 02:59", "2026-10-17 03:00"},
		{"30 9 * * mon-fri", "2026-10-17 10:00", "2026-10-19 09:30"}, // Sat → Mon
		{"0 0 1 * *", "2026-10-17 10:00", "2026-11-01 00:00"},
		{"0 0 1 jan *", "2026-10-17 10:00", "2027-01-01 00:00"},
		{"0 0 * * 7", "2026-10-17 10:00", "2026-10-18 00:00"}, // 7 is Sunday
		{"0 0 * * SUN", "2026-10-17 10:00", "2026-10-18 00:00"},
		{"5,10 1-3/2 * * *", "2026-10-17 01:10", "2026-10-17 03:05"},
		{"0 12 29 feb *", "2026-10-17 10:00", "2028-02-29 12:00"},
		{"0 0 31 * *", "2026-11-01 00:00", "2026-12-31 00:00"},
		// Vixie: both dom and dow restricted → either matches.
		{"0 0 13 * fri", "2026-10-17 10:00", "2026-10-23 00:00"}, // next Friday before the 13th
		{"0 0 13 * fri", "2026-11-10 00:00", "2026-11-13 00:00"},
		{"0 0 13 * 1", "2026-11-03 00:00", "2026-11-09 00:00"},
		// dom restricted, dow * → dom only.
		{"0 0 13 * *", "2026-10-17 10:00", "2026-11-13 00:00"},
		// dow restricted, dom * → dow only.
		{"0 0 * * 1", "2026-10-17 10:00", "2026-10-19 00:00"},
		{"@hourly", "2026-10-17 10:00", "2026-10-17 11:00"},
		{"@daily", "2026-10-17 10:00", "2026-10-18 00:00"},
		{"@midnight", "2026-10-17 10:00", "2026-10-18 00:00"},
		{"@weekly", "2026-10-17 10:00", "2026-10-18 00:00"},
		{"@monthly", "2026-10-17 10:00", "2026-11-01 00:00"},
		{"@yearly", "2026-10-17 10:00", "2027-01-01 00:00"},
		{"@annually", "2026-10-17 10:00", "2027-01-01 00:00"},
		{"0 0 * * 5-7", "2026-10-17 10:00", "2026-10-18 00:00"},
		{"0 0 * * */2", "2026-10-17 10:00", "2026-10-18 00:00"},   // 0,2,4,6 → Sun
		{"0 0 * * 1-5/2", "2026-10-17 10:00", "2026-10-19 00:00"}, // 1,3,5 → Mon
	}
	for _, tt := range tests {
		s, err := Parse(tt.spec)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.spec, err)
			continue
		}
		got := s.Next(at(utc, tt.after))
		if want := at(utc, tt.want); !got.Equal(want) {
			t.Errorf("%q after %s = %s, want %s", tt.spec, tt.after, got.Format("2006-01-02 15:04 Mon"), tt.want)
		}
	}
}

func TestNextIgnoresSecondsAndIsStrictlyAfter(t *testing.T) {
	s, _ := Parse("* * * * *")
	after := time.Date(2026, 10, 17, 10, 0, 30, 500, time.UTC)
	if got, want := s.Next(after), time.Date(2026, 10, 17, 10, 1, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestNextKeepsLocation(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	s, _ := Parse("0 9 * * *")
	got := s.Next(at(ny, "2026-10-17 10:00"))
	if got.Location() != ny {
		t.Fatalf("location %v, want %v", got.Location(), ny)
	}
	if got.Hour() != 9 || got.Day() != 18 {
		t.Fatalf("got %s", got)
	}
}

func TestDST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08: clocks jump 02:00 → 03:00 in New York.
	// 2026-11-01: clocks fall back 02:00 → 01:00.
	tests := []struct {
		name, spec string
		after      time.Time
		want       time.Time
	}{
		{
			// 02:30 doesn't exist on spring-forward day; skip to the next day.
			"spring gap skipped", "30 2 * * *",
			time.Date(2026, 3, 7, 12, 0, 0, 0, ny),
			time.Date(2026, 3, 9, 2, 30, 0, 0, ny),
		},
		{
			"spring hourly", "0 * * * *",
			time.Date(2026, 3, 8, 1, 30, 0, 0, ny),
			time.Date(2026, 3, 8, 3, 0, 0, 0, ny),
		},
		{
			"spring daily 3am fires", "0 3 * * *",
			time.Date(2026, 3, 7, 12, 0, 0, 0, ny),
			time.Date(2026, 3, 8, 3, 0, 0, 0, ny),
		},
		{
			// 01:30 happens twice on fall-back day; fire at the first one only.
			"fall back first occurrence", "30 1 * * *",
			time.Date(2026, 10, 31, 12, 0, 0, 0, ny),
			time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), // 01:30 EDT
		},
	}
	for _, tt := range tests {
		s, err := Parse(tt.spec)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Next(tt.after); !got.Equal(tt.want) {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}

	// After the first 01:30 on fall-back day, the next run is the following
	// day: the repeated wall-clock hour doesn't fire twice.
	s, _ := Parse("30 1 * * *")
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).In(ny) // 01:30 EDT
	next := s.Next(first)
	if want := time.Date(2026, 11, 2, 1, 30, 0, 0, ny); !next.Equal(want) {
		t.Errorf("after first fall-back 01:30: got %s, want %s", next, want)
	}

	// Every-minute schedules keep advancing in absolute time across the gap.
	s, _ = Parse("* * * * *")
	before := time.Date(2026, 3, 8, 1, 59, 0, 0, ny)
	if got := s.Next(before); got.Sub(before) != time.Minute {
		t.Errorf("minute across spring gap: got %s (%v later)", got, got.Sub(before))
	}
}

func TestNextNone(t *testing.T) {
	s, err := Parse("0 0 30 feb *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(at(time.UTC, "2026-10-17 10:00")); !got.IsZero() {
		t.Fatalf("Feb 30 should never fire, got %s", got)
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"-1 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 0 *",
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"*/-1 * * * *",
		"5-1 * * * *",
		"* * * foo *",
		"* * * * funday",
		"a * * * *",
		"1,,2 * * * *",
		"1- * * * *",
		"@reboot",
		"@every 5m",
		"1/2/3 * * * *",
		"*/5x * * * *",
		"0 0 * jan-foo *",
	}
	for _, spec := range bad {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", spec)
		}
	}
}

func TestParseCaseAndSpacing(t *testing.T) {
	for _, spec := range []string{"0 0 * JAN Mon", "  0   0 *  jan   mon  ", "0\t0 * jan mon", "@DAILY"} {
		if _, err := Parse(spec); err != nil {
			t.Errorf("Parse(%q): %v", spec, err)
		}
	}
}

func TestString(t *testing.T) {
	s, _ := Parse("  */5   *  * * * ")
	if s.String() != "*/5 * * * *" {
		t.Errorf("String() = %q", s.String())
	}
	s, _ = Parse("@daily")
	if s.String() != "@daily" {
		t.Errorf("String() = %q", s.String())
	}
}

func TestStartStepAndRepeatedHour(t *testing.T) {
	s, err := Parse("5/20 * * * *") // 5, 25, 45
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Next(at(time.UTC, "2026-10-17 10:30")), at(time.UTC, "2026-10-17 10:45"); !got.Equal(want) {
		t.Errorf("5/20: got %s, want %s", got, want)
	}

	ny := mustLoc(t, "America/New_York")
	s, _ = Parse("30 1 * * *")
	second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).In(ny) // 01:30 EST, the repeat
	if got, want := s.Next(second), time.Date(2026, 11, 2, 1, 30, 0, 0, ny); !got.Equal(want) {
		t.Errorf("after repeated 01:30: got %s, want %s", got, want)
	}
}
