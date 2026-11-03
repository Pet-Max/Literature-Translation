package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancellationStopsWorkersAndWrites(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.md")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := TranslateContext(ctx, TranslateOptions{MDText: strings.Repeat("# Section\n\n"+strings.Repeat("test words ", 400)+"\n\n", 8), OutPath: out, LLM: LLMOptions{BaseURL: srv.URL, APIKey: "temporary-key", Model: "mock", Concurrency: 2}})
		done <- e
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("no requests")
		}
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not stop")
	}
	count := requests.Load()
	before, _ := os.ReadDir(dir)
	time.Sleep(100 * time.Millisecond)
	after, _ := os.ReadDir(dir)
	if requests.Load() != count || len(after) != len(before) {
		t.Fatal("background work continued after return")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("cancelled task wrote final translation")
	}
}

func TestCancellationInterruptsBackoff(t *testing.T) {
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; w.WriteHeader(429) }))
	defer srv.Close()
	c := NewLLMClient(srv.URL, "temporary-key", "mock", 3, 4)
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := c.CompleteContext(ctx, "system", "user", 0.1, nil); done <- e }()
	<-started
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("backoff ignored cancellation")
	}
}
