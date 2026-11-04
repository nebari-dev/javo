package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A route that keeps writing (a log that follows a running container) is
// cut after maxResult bytes: the request is cancelled and the tool answers
// an error, instead of the gateway holding the whole stream in memory.
func TestCallStopsAnAnswerThatGrowsPastTheCap(t *testing.T) {
	chunk := []byte(strings.Repeat("y\n", 4096))
	var written int
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for r.Context().Err() == nil && written < 64*maxResult {
			n, err := w.Write(chunk)
			written += n
			if err != nil {
				return
			}
		}
	})
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		res, err := Config{API: api}.call(context.Background(), &sdk.CallToolRequest{}, http.MethodGet, "/api/jobs/x/runs/1/log", nil)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	select {
	case res := <-done:
		if res == nil || !res.IsError {
			t.Fatalf("result = %+v, want an error result", res)
		}
		if text := res.Content[0].(*sdk.TextContent).Text; len(text) > 1024 {
			t.Fatalf("error text holds the body: %d bytes", len(text))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call kept reading")
	}
	if written > 2*maxResult {
		t.Fatalf("the route wrote %d bytes after the cap of %d", written, maxResult)
	}
}

// An answer within the cap is returned whole.
func TestCallReturnsAnAnswerWithinTheCap(t *testing.T) {
	body := `{"name":"sales"}`
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	res, err := Config{API: api}.call(context.Background(), &sdk.CallToolRequest{}, http.MethodGet, "/api/workspaces/sales", nil)
	if err != nil || res.IsError || res.Content[0].(*sdk.TextContent).Text != body || res.StructuredContent == nil {
		t.Fatalf("res = %+v, %v", res, err)
	}
}
