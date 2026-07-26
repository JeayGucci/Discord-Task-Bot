package bot

import "testing"

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
