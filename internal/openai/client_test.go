package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRespondParsesFunctionCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "create_reminder") {
			t.Error("request does not contain tool")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","output":[{"type":"function_call","name":"create_reminder","arguments":{"title":"Test","description":"","delivery_at":"2030-01-01T12:00:00-05:00"}}],"usage":{"input_tokens":20,"output_tokens":10}}`))
	}))
	defer server.Close()
	c := New("test-key", "test-model", server.URL)
	result, err := c.Respond(context.Background(), "remind me", Context{Now: time.Now(), Timezone: "America/New_York", UserID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action == nil || result.Action.Name != "create_reminder" {
		t.Fatalf("action = %#v", result.Action)
	}
	if result.ResponseID != "resp_123" {
		t.Fatalf("response ID = %q", result.ResponseID)
	}
}
