package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/logstore"
)

func TestWebhookDeliveryAndSignature(t *testing.T) {
	var mu sync.Mutex
	var gotSig, gotEvent, gotBody string
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotSig = r.Header.Get("X-CCG-Signature")
		gotEvent = r.Header.Get("X-CCG-Event")
		gotBody = string(b)
		mu.Unlock()
		close(done)
	}))
	defer ts.Close()

	targets := buildWebhookTargets([]config.Webhook{{
		URL:    ts.URL,
		Secret: "s3cret",
	}})
	s := &Server{hooks: targets}

	rec := logstore.Record{
		Time: time.Now(), Model: "m1", Provider: "p1", Status: 200,
		InTok: 10, OutTok: 20, CostUSD: 0.5,
	}
	s.notifyHooks(rec)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook not delivered")
	}

	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(gotBody))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Fatalf("signature = %s, want %s", gotSig, want)
	}
	if gotEvent != "usage" {
		t.Fatalf("event = %s", gotEvent)
	}
	var p hookPayload
	if err := json.Unmarshal([]byte(gotBody), &p); err != nil {
		t.Fatal(err)
	}
	if p.Provider != "p1" || p.InputTokens != 10 || p.OutputTokens != 20 {
		t.Fatalf("payload = %+v", p)
	}
}

func TestWebhookEventFiltering(t *testing.T) {
	errHits := 0
	usageHits := 0
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-CCG-Event") == "error" {
			errHits++
		} else {
			usageHits++
		}
	}))
	defer ts.Close()

	targets := buildWebhookTargets([]config.Webhook{{URL: ts.URL, Events: []string{"error"}}})
	s := &Server{hooks: targets}

	s.notifyHooks(logstore.Record{Time: time.Now(), Status: 200})
	s.notifyHooks(logstore.Record{Time: time.Now(), Status: 429, Error: "rate limited"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := errHits == 1 && usageHits == 0
		mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("filter broken: err=%d usage=%d", errHits, usageHits)
}

func TestEventForClassification(t *testing.T) {
	if eventFor(logstore.Record{Status: 200}) != "usage" {
		t.Fatal("200 must be usage")
	}
	if eventFor(logstore.Record{Status: 500}) != "error" {
		t.Fatal("5xx must be error")
	}
	if eventFor(logstore.Record{Status: 0}) != "error" {
		t.Fatal("transport failure must be error")
	}
}
