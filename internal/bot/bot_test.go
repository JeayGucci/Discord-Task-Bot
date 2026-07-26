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
			name:    "one minute from now",
			content: "create a reminder one minute from now to test",
			want:    now.Add(time.Minute),
		},
		{
			name:    "numeric min",
			content: "remind me 1 min from now to test",
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

func TestHasRelativeReminderTimeHint(t *testing.T) {
	if !hasRelativeReminderTimeHint("remind me in one minute") {
		t.Fatal("expected relative time hint")
	}
	if hasRelativeReminderTimeHint("remind me tomorrow at 9") {
		t.Fatal("did not expect relative duration hint")
	}
}

func TestParseUserTimeUsesEasternDSTRules(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		value      string
		wantOffset int
		wantUTC    time.Time
	}{
		{
			name:       "winter uses EST",
			value:      "2026-01-15 09:00",
			wantOffset: -5 * 60 * 60,
			wantUTC:    time.Date(2026, 1, 15, 14, 0, 0, 0, time.UTC),
		},
		{
			name:       "summer uses EDT",
			value:      "2026-07-15 09:00",
			wantOffset: -4 * 60 * 60,
			wantUTC:    time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUserTime(tt.value, fixedTimezone, now)
			if err != nil {
				t.Fatal(err)
			}
			_, offset := got.Zone()
			if offset != tt.wantOffset {
				t.Fatalf("offset = %d, want %d", offset, tt.wantOffset)
			}
			if !got.UTC().Equal(tt.wantUTC) {
				t.Fatalf("UTC time = %s, want %s", got.UTC(), tt.wantUTC)
			}
		})
	}
}

func TestParseRelativeReminderTimeCrossesDSTBoundary(t *testing.T) {
	loc, err := time.LoadLocation(fixedTimezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 8, 1, 59, 0, 0, loc)
	got, err := parseRelativeReminderTime("remind me in 2 minutes to check daylight savings", fixedTimezone, now)
	if err != nil {
		t.Fatal(err)
	}
	_, offset := got.Zone()
	if offset != -4*60*60 {
		t.Fatalf("offset = %d, want EDT offset", offset)
	}
	if got.Hour() != 3 || got.Minute() != 1 {
		t.Fatalf("local time = %s, want 2026-03-08 03:01 EDT", got)
	}
}
