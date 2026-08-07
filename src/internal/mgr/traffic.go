package mgr

import (
	"fmt"
	"time"
)

// TrafficInterval is how often the background sampler runs.
const TrafficInterval = 60 * time.Second

// SampleTraffic reads the current LXD network counters of every running
// container and advances each user's monthly transfer totals. Counter resets
// (container restart/reinstall) and the monthly rollover (period key change)
// are handled inside the DB update, so concurrent samplers (background
// goroutine and CLI) can never double-count: the delta is computed against the
// baselines stored in the database at statement time, not against values read
// in this process.
func (m *Manager) SampleTraffic() error {
	tm, err := m.lx.TrafficMap()
	if err != nil {
		return err
	}
	users, err := m.db.ListUsers()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	period := time.Now().UTC().Format("2006-01")
	var firstErr error
	for _, u := range users {
		t, ok := tm[u.Name]
		if !ok {
			// container stopped: no counters available, nothing to add
			continue
		}
		if err := m.db.ApplyTraffic(u.ID, period, t.Rx, t.Tx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// TrafficFor returns the user's monthly upload/download totals in bytes.
func (m *Manager) TrafficFor(userID int64) (up, down uint64) {
	tr, err := m.db.GetTraffic(userID)
	if err != nil {
		return 0, 0
	}
	return tr.Upload, tr.Download
}

// FormatGB renders bytes as GB with one decimal place (e.g. 12.3).
func FormatGB(bytes uint64) string {
	return fmt.Sprintf("%.1f", float64(bytes)/(1<<30))
}
