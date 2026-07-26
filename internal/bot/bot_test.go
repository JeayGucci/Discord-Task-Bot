package bot

import (
	"testing"
	"time"
)

func TestLooksLikeReminderRequest(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "reminder with relative time",
			content: "can you create a reminder in one minute to make sure chat reminding is working",
			want:    true,
		},
		{
			name:    "reminder without time",
			content: "can you create a reminder",
			want:    false,
		},
		{
			name:    "plain chat",
			content: "please state bot status",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeReminderRequest(tt.content); got != tt.want {
				t.Fatalf("looksLikeReminderRequest(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestParseRelativeReminderTime(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 50, 0, 0, time.FixedZone("EDT", -4*60*60))
	tests := []struct {
		name    string
		content string
		want    time.Time
	}{
		{
			name:    "one minute",
			content: "can you create a reminder in one minute to make sure chat reminding is working",
			want:    now.Add(time.Minute),
		},
		{
			name:    "numeric hours",
			content: "remind me in 2 hours to check the oven",
			want:    now.Add(2 * time.Hour),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRelativeReminderTime(tt.content, fixedTimezone, now)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("parseRelativeReminderTime() = %s, want %s", got, tt.want)
			}
		})
	}
}
