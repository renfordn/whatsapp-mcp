package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
)

// hermesReplyMarker prefixes every message the bridge sends on Hermes's behalf. Messages
// carrying it are never forwarded to Hermes or PA, which stops a reply triggering itself.
const hermesReplyMarker = "🤖 "

var (
	hermesTimeout = 15 * time.Minute // agent turns with tool calls can be slow
	hermesClient  = &http.Client{}
	hermesSlots   = make(chan struct{}, 2)
)

func hermesEnabled() bool { return os.Getenv("HERMES_API_URL") != "" }

// ownUsers lists this account's phone and LID user parts (the self chat can be addressed by either).
func ownUsers(client *whatsmeow.Client) []string {
	if client.Store.ID == nil {
		return nil
	}
	return []string{client.Store.ID.User, client.Store.LID.User}
}

func isOwnChat(chatUser string, own []string) bool {
	if chatUser == "" {
		return false
	}
	for _, u := range own {
		if u == chatUser {
			return true
		}
	}
	return false
}

// isHermesTrigger: a message you typed into your own "Message yourself" chat that Hermes did not send.
func isHermesTrigger(chatUser string, own []string, isFromMe bool, content string) bool {
	return isFromMe && isOwnChat(chatUser, own) && strings.TrimSpace(content) != "" &&
		!strings.HasPrefix(content, hermesReplyMarker)
}

// skipPAIngest keeps the Hermes conversation (prompts and replies) out of pa-topics.
func skipPAIngest(enabled bool, chatUser string, own []string, isFromMe bool) bool {
	return enabled && isFromMe && isOwnChat(chatUser, own)
}

type hermesResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

// isFreshForHermes skips self-chat messages whatsmeow replays from the offline backlog after a restart.
func isFreshForHermes(sent, now time.Time) bool { return now.Sub(sent) < 2*time.Minute }

// hermesAsk sends one turn to the Hermes API server; the named conversation keeps multi-turn memory server-side.
func hermesAsk(baseURL, key, conversation, input string) (string, error) {
	body, err := json.Marshal(map[string]string{"input": input, "conversation": conversation})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), hermesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(baseURL, "/")+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := hermesClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("hermes status %d", resp.StatusCode)
	}
	var r hermesResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, o := range r.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			if c.Type == "output_text" {
				sb.WriteString(c.Text)
			}
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", fmt.Errorf("hermes returned no text")
	}
	return text, nil
}

// hermesHandle answers a self-chat message in a goroutine and posts the reply back into the same chat.
// Drops the message when two turns are already in flight.
func hermesHandle(client *whatsmeow.Client, chatJID, content string) {
	select {
	case hermesSlots <- struct{}{}:
	default:
		go sendWhatsAppMessage(client, chatJID, hermesReplyMarker+"Busy with an earlier message, send that again in a minute.", "")
		return
	}
	go func() {
		defer func() { <-hermesSlots }()
		conv := os.Getenv("HERMES_CONVERSATION")
		if conv == "" {
			conv = "whatsapp-self"
		}
		text, err := hermesAsk(os.Getenv("HERMES_API_URL"), os.Getenv("HERMES_API_KEY"), conv, content)
		if err != nil {
			fmt.Printf("hermes failed: %v\n", err)
			text = "Hermes couldn't answer (" + err.Error() + ")."
		}
		if ok, msg := sendWhatsAppMessage(client, chatJID, hermesReplyMarker+text, ""); !ok {
			fmt.Printf("hermes reply not sent: %s\n", msg)
		}
	}()
}
