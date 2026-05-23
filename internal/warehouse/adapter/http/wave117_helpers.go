package http

import "time"

// timeParseDate parses an YYYY-MM-DD date string and returns the start
// of that day in UTC.
func timeParseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

// timeHours returns a duration representing N hours.
func timeHours(n int) time.Duration {
	return time.Duration(n) * time.Hour
}
