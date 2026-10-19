package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/documentimport"
)

func TestImportCorpusRetriesInterruptedBatchAndResumesFromCheckpoint(t *testing.T) {
	input := filepath.Join(t.TempDir(), "fixture.jsonl")
	writeInterchangeFixture(t, input, []documentimport.Record{
		cliTestRecord("fixture-a", "https://a.example.test/page"),
		cliTestRecord("fixture-b", "https://b.example.test/page"),
	})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("import request did not use the configured bearer token")
		}
		var request documentimport.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Records) != 2 {
			t.Errorf("request records = %d, want 2", len(request.Records))
		}
		if calls == 1 {
			http.Error(w, "retryable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(importResponse{Results: []documentimport.Result{
			{ID: "fixture-a", Status: "imported"},
			{ID: "fixture-b", Status: "already-present"},
		}})
	}))
	defer server.Close()
	checkpoint := filepath.Join(t.TempDir(), "progress.json")
	options := importOptions{
		Input:    input,
		Endpoint: server.URL,
		APIKey:   "fixture-token",
		State:    checkpoint,
	}
	if err := importCorpus(context.Background(), options, io.Discard); err == nil {
		t.Fatal("first interrupted import succeeded")
	}
	state, found, err := loadImportCheckpoint(checkpoint)
	if err != nil || !found || state.Records != 0 {
		t.Fatalf("checkpoint after interruption = %+v, found %t, err %v", state, found, err)
	}
	if err := importCorpus(context.Background(), options, io.Discard); err != nil {
		t.Fatalf("resume import: %v", err)
	}
	state, found, err = loadImportCheckpoint(checkpoint)
	if err != nil || !found || state.Records != 2 {
		t.Fatalf("checkpoint after resume = %+v, found %t, err %v", state, found, err)
	}
	if calls != 2 {
		t.Fatalf("endpoint calls = %d, want failed attempt plus resumed attempt", calls)
	}
	if err := importCorpus(context.Background(), options, io.Discard); err != nil {
		t.Fatalf("completed re-run: %v", err)
	}
	if calls != 2 {
		t.Fatalf("completed import replayed %d batches", calls-2)
	}
}

func TestImportCorpusStopsAtConfiguredSuccessfulRecordLimit(t *testing.T) {
	input := filepath.Join(t.TempDir(), "bounded-run.jsonl")
	writeInterchangeFixture(t, input, []documentimport.Record{
		cliTestRecord("fixture-a", "https://a.example.test/"),
		cliTestRecord("fixture-b", "https://b.example.test/"),
		cliTestRecord("fixture-c", "https://c.example.test/"),
	})
	var received int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request documentimport.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode bounded import request: %v", err)
			return
		}
		received += len(request.Records)
		results := make([]documentimport.Result, len(request.Records))
		for index, record := range request.Records {
			results[index] = documentimport.Result{ID: record.ID, Status: "imported"}
		}
		_ = json.NewEncoder(w).Encode(importResponse{Results: results})
	}))
	defer server.Close()
	statePath := filepath.Join(t.TempDir(), "bounded-state.json")
	options := importOptions{
		Input: input, Endpoint: server.URL, APIKey: "fixture-token", State: statePath,
		BatchRecords: 1, MaxRecords: 2,
	}
	if err := importCorpus(t.Context(), options, io.Discard); err != nil {
		t.Fatalf("bounded import run: %v", err)
	}
	state, found, err := loadImportCheckpoint(statePath)
	if err != nil || !found || state.Records != 2 || state.Complete || received != 2 {
		t.Fatalf("bounded run checkpoint=%+v found=%t received=%d err=%v",
			state, found, received, err)
	}
	if err := importCorpus(t.Context(), options, io.Discard); err != nil {
		t.Fatalf("resumed bounded import run: %v", err)
	}
	state, found, err = loadImportCheckpoint(statePath)
	if err != nil || !found || state.Records != 3 || state.Complete || received != 3 {
		t.Fatalf("resumed checkpoint=%+v found=%t received=%d err=%v",
			state, found, received, err)
	}
}

func TestImportCorpusFollowsOnlyCommittedExportBytesUntilComplete(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "growing.jsonl")
	exportStatePath := filepath.Join(directory, "export.state.json")
	importStatePath := filepath.Join(directory, "import.state.json")
	header, err := json.Marshal(
		interchangeHeader{Type: "header", Version: 1, SourceID: "fixture-source"},
	)
	if err != nil {
		t.Fatal(err)
	}
	header = append(header, '\n')
	if err := os.WriteFile(input, header, 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint := exportCheckpoint{
		Version: 1, SourceID: "fixture-source", SourceFingerprint: "fixture-fingerprint",
		Cursor: "*", Offset: int64(len(header)),
	}
	if err := saveCheckpoint(exportStatePath, checkpoint); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request documentimport.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode import request: %v", err)
			return
		}
		requests.Add(1)
		results := make([]documentimport.Result, len(request.Records))
		for index, record := range request.Records {
			results[index] = documentimport.Result{ID: record.ID, Status: "imported"}
		}
		_ = json.NewEncoder(w).Encode(importResponse{Results: results})
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- importCorpus(ctx, importOptions{
			Input: input, Endpoint: server.URL, APIKey: "fixture-token", State: importStatePath,
			FollowExportCheckpoint: exportStatePath,
		}, io.Discard)
	}()

	appendExportRecord := func(record documentimport.Record, cursor string, complete bool) {
		t.Helper()
		file, err := os.OpenFile(input, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(file).Encode(
			interchangeRecord{Type: "document", Version: 1, Record: record},
		); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		info, err := file.Stat()
		if closeErr := file.Close(); err != nil || closeErr != nil {
			t.Fatalf("inspect or close append file: stat=%v close=%v", err, closeErr)
		}
		checkpoint.Cursor = cursor
		checkpoint.Offset = info.Size()
		checkpoint.Records++
		checkpoint.Complete = complete
		if err := saveCheckpoint(exportStatePath, checkpoint); err != nil {
			t.Fatal(err)
		}
	}

	// Bytes not yet committed by the exporter must remain invisible to the importer.
	first := cliTestRecord("fixture-a", "https://a.example.test/")
	file, err := os.OpenFile(input, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(
		interchangeRecord{Type: "document", Version: 1, Record: first},
	); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("importer read uncommitted export bytes: requests=%d", got)
	}
	info, err := os.Stat(input)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Cursor = "cursor-a"
	checkpoint.Offset = info.Size()
	checkpoint.Records = 1
	if err := saveCheckpoint(exportStatePath, checkpoint); err != nil {
		t.Fatal(err)
	}
	waitForImportRequests(t, &requests, 1)
	select {
	case err := <-done:
		t.Fatalf("import finished before export completed: %v", err)
	default:
	}

	appendExportRecord(
		cliTestRecord("fixture-b", "https://b.example.test/"), "cursor-b", true,
	)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow import: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("follow import did not finish after exporter marked complete")
	}
	state, found, err := loadImportCheckpoint(importStatePath)
	if err != nil || !found || !state.Complete || state.Records != 2 ||
		state.Offset != checkpoint.Offset || state.FollowExportOffset != checkpoint.Offset {
		t.Fatalf("final follow checkpoint = %+v found=%t err=%v", state, found, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("import requests=%d, want one committed batch per export page", got)
	}
}

func waitForImportRequests(t *testing.T, requests *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if requests.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d import request(s); got %d", want, requests.Load())
}

func TestImportCorpusRejectsMalformedRowsWithoutSendingOrAdvancing(t *testing.T) {
	input := filepath.Join(t.TempDir(), "malformed.jsonl")
	header, _ := json.Marshal(
		interchangeHeader{Type: "header", Version: 1, SourceID: "fixture-source"},
	)
	if err := os.WriteFile(
		input,
		append(append(header, '\n'), []byte("not-json\n")...),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }),
	)
	defer server.Close()
	statePath := filepath.Join(t.TempDir(), "checkpoint.json")
	err := importCorpus(context.Background(), importOptions{
		Input: input, Endpoint: server.URL, APIKey: "fixture-token", State: statePath,
	}, io.Discard)
	if err == nil {
		t.Fatal("malformed row was accepted")
	}
	state, found, stateErr := loadImportCheckpoint(statePath)
	if stateErr != nil || !found || state.Records != 0 || calls != 0 {
		t.Fatalf(
			"malformed row advanced import: state=%+v found=%t err=%v calls=%d",
			state,
			found,
			stateErr,
			calls,
		)
	}
}

func TestImportCorpusDryRunDoesNotWriteOrSend(t *testing.T) {
	input := filepath.Join(t.TempDir(), "dry-run.jsonl")
	writeInterchangeFixture(
		t,
		input,
		[]documentimport.Record{cliTestRecord("fixture", "https://dry.example.test/")},
	)
	calls := 0
	server := httptest.NewServer(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }),
	)
	defer server.Close()
	statePath := importCheckpointPath(input)
	var progress strings.Builder
	if err := importCorpus(context.Background(), importOptions{
		Input: input, Endpoint: server.URL, State: statePath, DryRun: true,
	}, &progress); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) || calls != 0 ||
		!strings.Contains(progress.String(), "validated 1") {
		t.Fatalf(
			"dry-run changed state or destination: state err=%v calls=%d output=%q",
			err,
			calls,
			progress.String(),
		)
	}
}

func TestImportCorpusDoesNotCheckpointMismatchedDestinationReceipt(t *testing.T) {
	input := filepath.Join(t.TempDir(), "wrong-receipt.jsonl")
	writeInterchangeFixture(
		t,
		input,
		[]documentimport.Record{cliTestRecord("fixture", "https://receipt.example.test/")},
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).
			Encode(importResponse{Results: []documentimport.Result{{ID: "wrong", Status: "imported"}}})
	}))
	defer server.Close()
	statePath := filepath.Join(t.TempDir(), "progress.json")
	if err := importCorpus(context.Background(), importOptions{
		Input: input, Endpoint: server.URL, APIKey: "fixture-token", State: statePath,
	}, io.Discard); err == nil {
		t.Fatal("mismatched receipt was accepted")
	}
	state, found, err := loadImportCheckpoint(statePath)
	if err != nil || !found || state.Records != 0 {
		t.Fatalf("bad receipt advanced checkpoint: %+v found=%t err=%v", state, found, err)
	}
}

func TestImportResumeRejectsDifferentDestination(t *testing.T) {
	input := filepath.Join(t.TempDir(), "destination.jsonl")
	writeInterchangeFixture(
		t,
		input,
		[]documentimport.Record{cliTestRecord("fixture", "https://destination.example.test/")},
	)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	options := importOptions{Input: input, Endpoint: server.URL, APIKey: "fixture-token"}
	if err := importCorpus(context.Background(), options, io.Discard); err == nil {
		t.Fatal("first import unexpectedly completed")
	}
	options.Endpoint = server.URL + "/different-node"
	if err := importCorpus(context.Background(), options, io.Discard); err == nil {
		t.Fatal("import resumed against a different destination")
	}
	if calls != 1 {
		t.Fatalf("destination received %d requests, want only the failed initial request", calls)
	}
}

func writeInterchangeFixture(t *testing.T, path string, records []documentimport.Record) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(
		interchangeHeader{Type: "header", Version: 1, SourceID: "fixture-source"},
	); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := encoder.Encode(
			interchangeRecord{Type: "document", Version: 1, Record: record},
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(
		path,
		time.Unix(1_800_000_000, 0),
		time.Unix(1_800_000_000, 0),
	); err != nil {
		t.Fatal(err)
	}
}

func cliTestRecord(id, rawURL string) documentimport.Record {
	return documentimport.Record{
		ID: id,
		Document: yagocrawlcontract.DocumentIngest{
			NormalizedURL: rawURL,
			CanonicalURL:  rawURL,
			ExtractedText: "safe synthetic document text",
			ContentHash:   "fixture-hash",
		},
	}
}
