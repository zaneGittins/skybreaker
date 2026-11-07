package system

import "time"

type Storage struct {
	Workspace uint64    `json:"workspace"`
	Worktrees uint64    `json:"worktrees"`
	Home      uint64    `json:"home"`
	Docker    uint64    `json:"docker"`
	GoCache   uint64    `json:"go_cache"`
	Total     uint64    `json:"total"`
	ScannedAt time.Time `json:"scanned_at"`
}

type Cleanup struct {
	Go            bool `json:"go"`
	Docker        bool `json:"docker"`
	OlderThanDays int  `json:"older_than_days"`
}

type Cleaned struct {
	Before    Storage  `json:"before"`
	After     *Storage `json:"after"`
	Recovered uint64   `json:"recovered"`
	Error     string   `json:"error,omitempty"`
}
