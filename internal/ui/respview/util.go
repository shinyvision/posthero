package respview

import (
	"fmt"
	"time"
)

func unixTime(n int64) time.Time { return time.Unix(n, 0) }

// relative says how far a time is from now: "in 4m", "3h ago".
func relative(t time.Time) string {
	d := time.Until(t)
	past := d < 0
	if past {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		s = fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		s = fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		s = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if past {
		return s + " ago"
	}
	return "in " + s
}
