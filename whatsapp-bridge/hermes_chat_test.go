package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHermesTriggerOnlyOwnSelfChatWithoutReplyMarker(t *testing.T) {
	own := []string{"447700900000", "123456789"}
	cases := []struct {
		name     string
		chatUser string
		fromMe   bool
		content  string
		want     bool
	}{
		{"self chat by phone", "447700900000", true, "what's on today?", true},
		{"self chat by LID", "123456789", true, "hi", true},
		{"hermes reply is ignored", "447700900000", true, hermesReplyMarker + "hello", false},
		{"other chat", "447700900111", true, "hi", false},
		{"not from me", "447700900000", false, "hi", false},
		{"blank", "447700900000", true, "   ", false},
	}
	for _, c := range cases {
		if got := isHermesTrigger(c.chatUser, own, c.fromMe, c.content); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestHermesTriggerIgnoresEmptyOwnIDs(t *testing.T) {
	if isHermesTrigger("", []string{"", "447700900000"}, true, "hi") {
		t.Fatal("empty chat user must not match an empty own id")
	}
}

func TestSkipPAIngestForHermesChat(t *testing.T) {
	own := []string{"447700900000"}
	if !skipPAIngest(true, "447700900000", own, true) {
		t.Fatal("hermes prompt should stay out of PA")
	}
	if !skipPAIngest(true, "447700900000", own, true) {
		t.Fatal("hermes reply should stay out of PA")
	}
	if skipPAIngest(false, "447700900000", own, true) {
		t.Fatal("self chat goes to PA when hermes is off")
	}
	if skipPAIngest(true, "447700900111", own, true) {
		t.Fatal("other chats still go to PA")
	}
}

func TestHermesAskPostsConversationAndParsesOutput(t *testing.T) {
	var got map[string]interface{}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"output":[{"type":"reasoning"},{"type":"message","content":[{"type":"output_text","text":"Hello"},{"type":"output_text","text":" there"}]}]}`))
	}))
	defer srv.Close()
	text, err := hermesAsk(srv.URL, "key", "whatsapp-self", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello there" {
		t.Fatalf("text %q", text)
	}
	if auth != "Bearer key" || got["input"] != "hi" || got["conversation"] != "whatsapp-self" {
		t.Fatalf("bad request auth=%q body=%v", auth, got)
	}
}

func TestHermesAskErrorsOnBadStatusOrEmptyOutput(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	if _, err := hermesAsk(bad.URL, "k", "c", "hi"); err == nil {
		t.Fatal("want error on 401")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":[]}`))
	}))
	defer empty.Close()
	if _, err := hermesAsk(empty.URL, "k", "c", "hi"); err == nil {
		t.Fatal("want error on empty output")
	}
}

func TestHermesSkipsStaleBacklogMessages(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if !isFreshForHermes(now.Add(-30*time.Second), now) {
		t.Fatal("a message from 30s ago should be answered")
	}
	if isFreshForHermes(now.Add(-10*time.Minute), now) {
		t.Fatal("a message replayed from the offline backlog should be skipped")
	}
}
