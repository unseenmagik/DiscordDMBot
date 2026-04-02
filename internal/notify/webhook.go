package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

type DiscordWebhook struct {
	url    string
	client *http.Client
	logger *log.Logger
}

type Field struct {
	Name   string
	Value  string
	Inline bool
}

func NewDiscordWebhook(url string, logger *log.Logger) *DiscordWebhook {
	if url == "" {
		return nil
	}

	return &DiscordWebhook{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

func (w *DiscordWebhook) Enabled() bool {
	return w != nil && w.url != ""
}

func (w *DiscordWebhook) Send(title, description string, color int, fields []Field) error {
	if !w.Enabled() {
		return nil
	}

	type embedField struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Inline bool   `json:"inline"`
	}
	type embed struct {
		Title       string       `json:"title,omitempty"`
		Description string       `json:"description,omitempty"`
		Color       int          `json:"color,omitempty"`
		Timestamp   string       `json:"timestamp,omitempty"`
		Fields      []embedField `json:"fields,omitempty"`
	}
	type payload struct {
		Username string  `json:"username,omitempty"`
		Embeds   []embed `json:"embeds"`
	}

	embedFields := make([]embedField, 0, len(fields))
	for _, field := range fields {
		embedFields = append(embedFields, embedField{
			Name:   trim(field.Name, 256),
			Value:  trim(field.Value, 1024),
			Inline: field.Inline,
		})
	}
	if len(embedFields) > 25 {
		embedFields = embedFields[:25]
	}

	body, err := json.Marshal(payload{
		Username: "DM Reminder Bot",
		Embeds: []embed{
			{
				Title:       trim(title, 256),
				Description: trim(description, 4096),
				Color:       color,
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
				Fields:      embedFields,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	resp, err := w.client.Post(w.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %s", resp.Status)
	}

	return nil
}

func trim(value string, max int) string {
	if len(value) <= max {
		return value
	}

	return value[:max]
}
