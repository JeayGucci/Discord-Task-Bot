package reminders

import (
	"testing"
	"time"
)

func TestCreateParamsValidate(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	valid := CreateParams{Title: "SOAP note", CreatorID: "1", ChannelID: "2", DeliveryAt: now.Add(time.Hour), Timezone: "America/New_York"}
	if err := valid.Validate(now); err != nil {
		t.Fatalf("valid params: %v", err)
	}
	valid.DeliveryAt = now
	if err := valid.Validate(now); err == nil {
		t.Fatal("expected past delivery error")
	}
}

func TestTimezoneDSTConversion(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	winter := time.Date(2026, 1, 15, 9, 0, 0, 0, loc)
	summer := time.Date(2026, 7, 15, 9, 0, 0, 0, loc)
	_, winterOffset := winter.Zone()
	_, summerOffset := summer.Zone()
	if winterOffset == summerOffset {
		t.Fatal("expected DST offset difference")
	}
}
