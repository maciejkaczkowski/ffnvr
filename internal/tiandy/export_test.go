package tiandy

import "time"

// SetEventTimings shortens the alarm hold and the quiet that ends the burst after the
// login; the returned function restores them.
func SetEventTimings(hold, quiet time.Duration) func() {
	h, q := AlarmHold, burstQuiet
	AlarmHold, burstQuiet = hold, quiet
	return func() { AlarmHold, burstQuiet = h, q }
}
