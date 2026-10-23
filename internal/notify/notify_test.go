package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramNotifyPostsFormEncodedMessage(t *testing.T) {
	var gotPath, gotBody, gotType, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(b)
		gotType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := NewTelegram("123:abc", "-4242").(*telegram)
	n.baseURL = srv.URL

	if err := n.Notify(context.Background(), "triage run ok"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/bot123:abc/sendMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if gotType != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", gotType)
	}
	for _, want := range []string{"chat_id=-4242", "text=triage+run+ok", "disable_web_page_preview=true"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %q missing %q", gotBody, want)
		}
	}
}

func TestTelegramNotifyReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	n := NewTelegram("tok", "chat").(*telegram)
	n.baseURL = srv.URL

	err := n.Notify(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error on 400")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error = %v", err)
	}
}

func TestTelegramNotifyRedactsTokenFromTransportErrors(t *testing.T) {
	n := NewTelegram("secret-token", "chat").(*telegram)
	n.baseURL = "http://127.0.0.1:1" // nothing listening

	err := n.Notify(context.Background(), "hi")
	if err == nil {
		t.Fatal("want transport error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("token leaked into error: %v", err)
	}
}

func TestNopNotifierSucceedsSilently(t *testing.T) {
	if err := Nop().Notify(context.Background(), "anything"); err != nil {
		t.Fatalf("Nop Notify: %v", err)
	}
}

func TestHealthcheckGETsURL(t *testing.T) {
	var hits int
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		method = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Healthcheck(context.Background(), srv.URL+"/ping/abc"); err != nil {
		t.Fatalf("Healthcheck: %v", err)
	}
	if hits != 1 || method != http.MethodGet {
		t.Errorf("hits = %d, method = %q", hits, method)
	}
}

func TestHealthcheckFailsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := Healthcheck(context.Background(), srv.URL); err == nil {
		t.Fatal("want error on 500")
	}
}
