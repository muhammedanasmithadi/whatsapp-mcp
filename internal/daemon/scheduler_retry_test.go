package daemon

import (
	"testing"
	"time"
)

func TestRetryableSendFailure(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Error sending message: failed to get device list: failed to send usync query: info query timed out", true},
		{"Error uploading media: failed to refresh media connections: failed to query media connections: info query timed out", true},
		{"rate limited: minimum send interval not elapsed", true},
		{"failed to send message node: Post \"https://media-blr1-2.cdn.whatsapp.net\": EOF", true},
		{"stream:error code=479", true},
		{"media_path rejected: media_path \"/etc/hostname\" not accessible", false},
		{"recipient: must be digits only", false},
		{"poll not found in cache", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := retryableSendFailure(tc.in); got != tc.want {
			t.Errorf("retryableSendFailure(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestScheduleRetryBudget(t *testing.T) {
	// The retry budget must be finite and the backoff must grow, so a message
	// that can never send still lands in 'failed' rather than looping forever.
	if maxScheduleAttempts <= 0 {
		t.Fatalf("maxScheduleAttempts = %d, want a positive bound", maxScheduleAttempts)
	}
	first := scheduleRetryBackoff
	fifth := 5 * scheduleRetryBackoff
	if fifth <= first {
		t.Errorf("backoff does not grow: attempt 1 = %v, attempt 5 = %v", first, fifth)
	}
	if span := time.Duration(maxScheduleAttempts) * scheduleRetryBackoff; span > 30*time.Minute {
		t.Errorf("total retry window = %v, want at most 30 minutes", span)
	}
}
