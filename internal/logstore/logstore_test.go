package logstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAddAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	s, err := New(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Add(Record{Time: time.Now(), Model: "m1", Provider: "p1", Status: 200, InTok: 10, OutTok: 5, CostUSD: 0.01})
	s.Add(Record{Time: time.Now(), Model: "m1", Provider: "p1", Status: 429, Error: "rate limited", InTok: 20, OutTok: 0})
	s.Add(Record{Time: time.Now(), Model: "m2", Provider: "p2", Status: 200, InTok: 100, OutTok: 50, CostUSD: 0.5})

	v := s.Snapshot()
	if v.Total.Requests != 3 {
		t.Fatalf("total requests = %d", v.Total.Requests)
	}
	if v.Total.Errors != 1 {
		t.Fatalf("total errors = %d", v.Total.Errors)
	}
	if v.Total.InTok != 130 {
		t.Fatalf("total in = %d", v.Total.InTok)
	}
	if v.Total.OutTok != 55 {
		t.Fatalf("total out = %d", v.Total.OutTok)
	}
	if v.ByModel["m1"].Requests != 2 {
		t.Fatalf("m1 requests = %d", v.ByModel["m1"].Requests)
	}
	if v.ByProvider["p1"].Errors != 1 {
		t.Fatalf("p1 errors = %d", v.ByProvider["p1"].Errors)
	}
	if len(v.Recent) != 3 {
		t.Fatalf("recent = %d", len(v.Recent))
	}
}

func TestRingBufferEviction(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "u.jsonl"), 5)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 10; i++ {
		s.Add(Record{Time: time.Now(), Model: "m", Status: 200})
	}
	v := s.Snapshot()
	if len(v.Recent) != 5 {
		t.Fatalf("ring = %d, want 5", len(v.Recent))
	}
	if v.Recent[0].Model != "m" && v.Total.Requests != 10 {
		t.Fatal("total must count all requests even after eviction")
	}
}

func TestHourlyAggregation(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "u.jsonl"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	s.Add(Record{Time: now, Model: "m", Status: 200})
	s.Add(Record{Time: now.Add(-time.Hour), Model: "m", Status: 200})

	v := s.Snapshot()
	if len(v.ByHour) < 2 {
		t.Fatalf("by_hour = %d, want >= 2", len(v.ByHour))
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "u.jsonl")

	s1, err := New(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	s1.Add(Record{Time: time.Now(), Model: "persisted", Status: 200})
	s1.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("invalid JSONL: %v", err)
	}
	if rec.Model != "persisted" {
		t.Fatal("wrong model in file")
	}
}

func TestCachedAndSemanticFlags(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "u.jsonl"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Add(Record{Time: time.Now(), Model: "m", Status: 200, Cached: true, SemanticHit: true})
	v := s.Snapshot()
	if !v.Recent[0].Cached || !v.Recent[0].SemanticHit {
		t.Fatal("cached/semantic flags lost")
	}
}
