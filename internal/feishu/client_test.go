package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSignMatchesKnownVector(t *testing.T) {
	got := Sign("test-secret", 1700000000)
	want := "mbm4Y4oluIPQ00qlBIhX8vAZ0EKv3nw0LuTb91jPL84="
	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
}

func TestSendTextSuccess(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", 5*time.Second)
	if err := c.SendText(context.Background(), "hello"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if got["msg_type"] != "text" {
		t.Fatalf("unexpected payload: %+v", got)
	}
	content := got["content"].(map[string]any)
	if content["text"] != "hello" {
		t.Fatalf("unexpected content: %+v", content)
	}
	if _, hasSign := got["sign"]; hasSign {
		t.Fatalf("unexpected sign without secret: %+v", got)
	}
}

func TestSendAddsSignatureWhenSecretSet(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"StatusCode":0,"StatusMessage":"success"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "s3cr3t", 5*time.Second)
	if err := c.SendText(context.Background(), "hi"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if got["sign"] == "" || got["sign"] == nil {
		t.Fatalf("missing sign: %+v", got)
	}
	if got["timestamp"] == "" || got["timestamp"] == nil {
		t.Fatalf("missing timestamp: %+v", got)
	}
}

func TestSendReportsLogicalFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":19001,"msg":"sign match fail"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", 5*time.Second)
	err := c.SendText(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "sign match fail") {
		t.Fatalf("expected logical failure, got %v", err)
	}
}

func TestSendRetriesOnServerError(t *testing.T) {
	old := retryBase
	retryBase = time.Millisecond
	defer func() { retryBase = old }()

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":-1,"msg":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", 5*time.Second)
	if err := c.SendText(context.Background(), "hi"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}
