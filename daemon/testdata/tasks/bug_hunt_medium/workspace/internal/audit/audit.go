// Package audit records what checkout did, for support staff.
package audit

import (
	"fmt"
	"io"
	"time"
)

// Log writes one line per event.
type Log struct {
	W   io.Writer
	Now func() time.Time
}

// Record writes event with its details.
func (l Log) Record(event string, details ...any) {
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	fmt.Fprintf(l.W, "%s %s %v\n", now().UTC().Format(time.RFC3339), event, details)
}
