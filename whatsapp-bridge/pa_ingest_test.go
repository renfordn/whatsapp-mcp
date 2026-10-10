package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func sampleMsg() paMessage {
	return paMessage{ID: "m1", ChatJID: "44@s.whatsapp.net", Sender: "44", ChatName: "Sam",
		Content: "hi", Timestamp: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC), IsFromMe: true, MediaType: ""}
}

func TestPAIngestPostsJSONWithBearer(t *testing.T) {
	var got map[string]interface{}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	if err := paPost(srv.URL, "tok", sampleMsg()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" {
		t.Fatalf("auth %q", auth)
	}
	for _, k := range []string{"id", "chat_jid", "sender", "chat_name", "content", "timestamp", "is_from_me", "media_type"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	if got["timestamp"] != "2026-10-08T10:00:00Z" || got["is_from_me"] != true {
		t.Fatalf("bad values %v", got)
	}
}

func TestPAIngestNoopWhenURLUnset(t *testing.T) {
	t.Setenv("PA_INGEST_URL", "")
	if paNotify(sampleMsg()) != nil {
		t.Fatal("expected no goroutine/no-op")
	}
}

func TestPAIngestErrorStatusReturnsError(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	if err := paPost(bad.URL, "t", sampleMsg()); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestPAIngestTimeout(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	old := paTimeout
	paTimeout = 50 * time.Millisecond
	defer func() { paTimeout = old }()
	start := time.Now()
	if err := paPost(slow.URL, "t", sampleMsg()); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("expected timeout, err=%v", err)
	}
}

func TestPAIngestNotifyDoesNotBlockAndDropsWhenSaturated(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	t.Setenv("PA_INGEST_URL", hang.URL)
	var dones []<-chan struct{}
	start := time.Now()
	for i := 0; i < cap(paSlots); i++ {
		d := paNotify(sampleMsg())
		if d == nil {
			t.Fatalf("slot %d unexpectedly dropped", i)
		}
		dones = append(dones, d)
	}
	if paNotify(sampleMsg()) != nil {
		t.Fatal("expected drop when saturated")
	}
	if time.Since(start) > time.Second {
		t.Fatal("paNotify blocked caller")
	}
	close(release)
	for _, d := range dones {
		<-d
	}
	if d := paNotify(sampleMsg()); d == nil {
		t.Fatal("slot not released")
	} else {
		<-d
	}
}
