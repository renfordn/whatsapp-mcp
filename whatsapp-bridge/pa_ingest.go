package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// paMessage is the pa-topics /api/ingest payload.
type paMessage struct {
	ID        string    `json:"id"`
	ChatJID   string    `json:"chat_jid"`
	Sender    string    `json:"sender"`
	ChatName  string    `json:"chat_name"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	IsFromMe  bool      `json:"is_from_me"`
	MediaType string    `json:"media_type"`
}

var (
	paTimeout = 3 * time.Second
	paClient  = &http.Client{} // shared; per-request deadline comes from paTimeout
	paSlots   = make(chan struct{}, 8)
)

func paPost(url, token string, m paMessage) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), paTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := paClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pa ingest status %d", resp.StatusCode)
	}
	return nil
}

// paNotify forwards a stored message to pa-topics in a goroutine. No-op (nil) when
// PA_INGEST_URL is unset. Never blocks the caller (drops when 8 attempts are in flight); failures are dropped (pa-topics backfills).
// The returned channel closes when the attempt finishes (used by tests).
func paNotify(m paMessage) <-chan struct{} {
	url := os.Getenv("PA_INGEST_URL")
	if url == "" {
		return nil
	}
	token := os.Getenv("PA_INGEST_TOKEN")
	select {
	case paSlots <- struct{}{}:
	default:
		return nil // too many in flight (outage + burst): drop; backfill covers it
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { <-paSlots }()
		if err := paPost(url, token, m); err != nil {
			fmt.Printf("pa ingest failed: %v\n", err)
		}
	}()
	return done
}
