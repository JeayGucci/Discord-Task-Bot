package ops

import (
	"context"
	"sync"
	"time"
)

type Event struct {
	Time       time.Time      `json:"time"`
	Level      string         `json:"level"`
	Source     string         `json:"source"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type Recorder struct {
	mu     sync.Mutex
	limit  int
	events []Event
	health map[string]any
}

func NewRecorder(limit int) *Recorder {
	if limit <= 0 {
		limit = 200
	}
	return &Recorder{limit: limit, health: map[string]any{}}
}

func (r *Recorder) Record(level, source, message string, attrs map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	event := Event{Time: time.Now().UTC(), Level: level, Source: source, Message: message, Attributes: attrs}
	r.events = append(r.events, event)
	if len(r.events) > r.limit {
		copy(r.events, r.events[len(r.events)-r.limit:])
		r.events = r.events[:r.limit]
	}
}

func (r *Recorder) List(limit int) []Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > len(r.events) {
		limit = len(r.events)
	}
	result := make([]Event, 0, limit)
	for i := len(r.events) - 1; i >= len(r.events)-limit; i-- {
		result = append(result, r.events[i])
	}
	return result
}

func (r *Recorder) SetHealth(key string, value any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.health[key] = value
}

func (r *Recorder) Health() map[string]any {
	if r == nil {
		return map[string]any{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]any, len(r.health))
	for key, value := range r.health {
		result[key] = value
	}
	return result
}

func Attributes(values ...any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	attrs := map[string]any{}
	for i := 0; i+1 < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok || key == "" {
			continue
		}
		attrs[key] = values[i+1]
	}
	return attrs
}

func (r *Recorder) LastSchedulerTick(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		r.SetHealth("scheduler_status", err.Error())
		return
	}
	r.SetHealth("scheduler_status", "running")
	r.SetHealth("last_scheduler_tick", time.Now().UTC())
}
