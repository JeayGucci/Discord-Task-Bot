package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

type Context struct {
	Now                time.Time
	Timezone           string
	UserID             string
	GuildID            string
	ChannelID          string
	PreviousResponseID string
}

type Action struct {
	Name      string
	Arguments json.RawMessage
}

type Result struct {
	Text       string
	Action     *Action
	Usage      Usage
	ResponseID string
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func New(apiKey, model, baseURL string) *Client {
	return &Client{apiKey: apiKey, model: model, baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 45 * time.Second}}
}

func (c *Client) Enabled() bool { return c.apiKey != "" }

func (c *Client) Respond(ctx context.Context, message string, meta Context) (Result, error) {
	if !c.Enabled() {
		return Result{}, errors.New("AI chat is not configured")
	}
	system := fmt.Sprintf(`You are TaskBot, a concise Discord bot that helps with reminders, to-dos, dashboard troubleshooting, and lightweight chat. Current time is %s and the user's IANA timezone is %s.

Capabilities:
- Create one future reminder with create_reminder when the user clearly provides a title and exact future time.
- Check sanitized runtime health with get_bot_status when the user asks whether the bot, dashboard, Discord connection, OpenAI, scheduler, channels, or reminder delivery are working.
- Explain available slash commands: /remind create, /remind list, /remind edit, /remind cancel, /remind complete, /reminders, /todo create, /timezone set, /chat reset, /dashboard, and /privacy delete-my-data.
- Explain that dashboard reminder creation defaults to Jeay and #general-to-do-list when configured.

Never claim an action succeeded; the application reports execution results. Ask a concise clarification question if the reminder title or exact future time is missing. Do not reveal secrets, tokens, passwords, raw environment values, or private system details. For ordinary conversation, answer normally and concisely. Do not provide professional medical advice.`, meta.Now.In(mustLocation(meta.Timezone)).Format(time.RFC3339), meta.Timezone)
	body := map[string]any{
		"model":             c.model,
		"instructions":      system,
		"input":             message,
		"max_output_tokens": 500,
		"safety_identifier": safetyIdentifier(meta.UserID),
		"tools": []any{
			map[string]any{
				"type": "function", "name": "create_reminder",
				"description": "Create one reminder only after the user clearly provides a title and future delivery time.",
				"parameters": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{
						"title":       map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
						"delivery_at": map[string]any{"type": "string", "description": "RFC3339 timestamp with UTC offset"},
					},
					"required": []string{"title", "description", "delivery_at"},
				},
			},
			map[string]any{
				"type":        "function",
				"name":        "get_bot_status",
				"description": "Read sanitized TaskBot runtime status for Discord, dashboard, OpenAI, scheduler, channels, and recent operational logs.",
				"parameters": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties":           map[string]any{},
				},
			},
		},
	}
	if meta.PreviousResponseID != "" {
		body["previous_response_id"] = meta.PreviousResponseID
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("OpenAI API returned %d: %s", resp.StatusCode, compact(data, 500))
	}
	var wire struct {
		ID         string `json:"id"`
		OutputText string `json:"output_text"`
		Usage      Usage  `json:"usage"`
		Output     []struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Result{}, err
	}
	result := Result{Text: wire.OutputText, Usage: wire.Usage, ResponseID: wire.ID}
	for _, item := range wire.Output {
		if item.Type == "function_call" {
			result.Action = &Action{Name: item.Name, Arguments: item.Arguments}
			return result, nil
		}
		if result.Text == "" {
			for _, content := range item.Content {
				if content.Type == "output_text" {
					result.Text += content.Text
				}
			}
		}
	}
	if result.Text == "" {
		return Result{}, errors.New("OpenAI returned no usable output")
	}
	return result, nil
}

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}
func compact(v []byte, max int) string {
	s := strings.Join(strings.Fields(string(v)), " ")
	if len(s) > max {
		return s[:max]
	}
	return s
}

func safetyIdentifier(userID string) string {
	sum := sha256.Sum256([]byte("taskbot:" + userID))
	return fmt.Sprintf("discord_%x", sum[:16])
}
