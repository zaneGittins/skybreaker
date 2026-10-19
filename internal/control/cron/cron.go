// Package cron parses standard five-field cron expressions and computes the
// next time a schedule fires.
//
// Times are matched on the wall clock of the location passed to Next. A wall
// time that doesn't exist (the hour skipped when clocks spring forward) never
// fires, and a wall time that happens twice (when clocks fall back) fires only
// at its first occurrence.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression.
type Schedule struct {
	spec             string
	minute, hour     uint64 // bit i set: value i matches
	dom, month, dow  uint64
	domStar, dowStar bool
}

var ErrSyntax = errors.New("cron: invalid expression")

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

type field struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	months = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	days   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	fields = [5]field{
		{"minute", 0, 59, nil},
		{"hour", 0, 23, nil},
		{"day of month", 1, 31, nil},
		{"month", 1, 12, months},
		{"day of week", 0, 7, days}, // 7 is Sunday too
	}
)

// Parse reads a five-field expression (minute hour day-of-month month
// day-of-week) or one of the @yearly, @monthly, @weekly, @daily, @midnight and
// @hourly macros.
func Parse(spec string) (*Schedule, error) {
	parts := strings.Fields(spec)
	canonical := strings.Join(parts, " ")
	if len(parts) == 1 && strings.HasPrefix(parts[0], "@") {
		m := strings.ToLower(parts[0])
		expansion, ok := macros[m]
		if !ok {
			return nil, fmt.Errorf("%w: unknown macro %q", ErrSyntax, parts[0])
		}
		parts, canonical = strings.Fields(expansion), m
	}
	if len(parts) != 5 {
		return nil, fmt.Errorf("%w: want 5 fields, got %d in %q", ErrSyntax, len(parts), spec)
	}
	s := &Schedule{spec: canonical}
	dst := [5]*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, p := range parts {
		bits, err := parseField(p, fields[i])
		if err != nil {
			return nil, err
		}
		*dst[i] = bits
	}
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	// As in Vixie cron, a field starting with '*' counts as unrestricted when
	// deciding whether day-of-month and day-of-week combine with OR.
	s.domStar = strings.HasPrefix(parts[2], "*")
	s.dowStar = strings.HasPrefix(parts[4], "*")
	return s, nil
}

func parseField(text string, f field) (uint64, error) {
	var bits uint64
	for _, item := range strings.Split(text, ",") {
		b, err := parseItem(strings.ToLower(item), f)
		if err != nil {
			return 0, fmt.Errorf("%w: %s %q: %v", ErrSyntax, f.name, text, err)
		}
		bits |= b
	}
	return bits, nil
}

func parseItem(item string, f field) (uint64, error) {
	rng, stepText, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("bad step %q", stepText)
		}
		step = n
	}
	lo, hi := f.min, f.max
	switch {
	case rng == "*":
	case rng == "":
		return 0, errors.New("empty value")
	default:
		loText, hiText, isRange := strings.Cut(rng, "-")
		var err error
		if lo, err = value(loText, f); err != nil {
			return 0, err
		}
		hi = lo
		if isRange {
			if hi, err = value(hiText, f); err != nil {
				return 0, err
			}
			if hi < lo {
				return 0, fmt.Errorf("range %q runs backwards", rng)
			}
		} else if hasStep {
			// "5/15" means from 5 to the end, every 15.
			hi = f.max
		}
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << v
	}
	return bits, nil
}

func value(text string, f field) (int, error) {
	if n, ok := f.names[text]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || strings.HasPrefix(text, "+") {
		return 0, fmt.Errorf("bad value %q", text)
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("%d out of range %d-%d", n, f.min, f.max)
	}
	return n, nil
}

// String returns the expression with normalised spacing, or the macro name.
func (s *Schedule) String() string { return s.spec }

// horizon bounds the search so impossible schedules such as Feb 30 end.
const horizon = 5 * 366

// Next returns the first time strictly after `after` (at second zero) when
// the schedule fires, in after's location, or the zero time if it doesn't
// fire within five years.
func (s *Schedule) Next(after time.Time) time.Time {
	loc := after.Location()
	y, m, d := after.Date()
	for i := 0; i <= horizon; i++ {
		day := time.Date(y, m, d+i, 12, 0, 0, 0, loc)
		if !s.dayMatches(day) {
			continue
		}
		dy, dm, dd := day.Date()
		for h := 0; h < 24; h++ {
			if s.hour&(1<<h) == 0 {
				continue
			}
			for min := 0; min < 60; min++ {
				if s.minute&(1<<min) == 0 {
					continue
				}
				t, ok := wallTime(dy, dm, dd, h, min, loc)
				if ok && t.After(after) {
					return t
				}
			}
		}
	}
	return time.Time{}
}

func (s *Schedule) dayMatches(t time.Time) bool {
	if s.month&(1<<int(t.Month())) == 0 {
		return false
	}
	domOK := s.dom&(1<<t.Day()) != 0
	dowOK := s.dow&(1<<int(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return domOK && dowOK
	}
	return domOK || dowOK
}

// wallTime returns the earliest instant whose wall clock in loc reads the
// given date and time, or false if that wall time doesn't exist.
func wallTime(y int, m time.Month, d, h, min int, loc *time.Location) (time.Time, bool) {
	t := time.Date(y, m, d, h, min, 0, 0, loc)
	if !sameWall(t, y, m, d, h, min) {
		return time.Time{}, false
	}
	// time.Date may pick either instant of a repeated wall time; prefer the
	// earlier one. Real-world shifts are 30 or 60 minutes.
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute} {
		if e := t.Add(-back); sameWall(e, y, m, d, h, min) {
			return e, true
		}
	}
	return t, true
}

func sameWall(t time.Time, y int, m time.Month, d, h, min int) bool {
	ty, tm, td := t.Date()
	return ty == y && tm == m && td == d && t.Hour() == h && t.Minute() == min
}
