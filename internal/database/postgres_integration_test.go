package database

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/JeayGucci/Discord-Task-Bot/internal/reminders"
)

func TestPostgresReminderLifecycle(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_PUBLIC_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_PUBLIC_URL is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, url, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	creator := "integration-" + time.Now().Format("150405.000000")
	delivery := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	created, err := store.Create(ctx, reminders.CreateParams{Title: "Integration", CreatorID: creator, ChannelID: "channel", MentionTarget: "<@owner>", DeliveryAt: delivery, Timezone: "America/New_York"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM reminders WHERE creator_id=$1`, creator)
	})
	listed, err := store.List(ctx, reminders.ListFilter{CreatorID: creator})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list len=%d err=%v", len(listed), err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE reminders SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDue(ctx, time.Now(), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim len=%d err=%v", len(claimed), err)
	}
	if err := store.MarkSent(ctx, created.ID, "message-id"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != reminders.StatusSent || got.DiscordMessageID != "message-id" {
		t.Fatalf("got=%+v", got)
	}
}
