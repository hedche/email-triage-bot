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
	for _, want := range []string{"chat_id=-4242", "text=triage+run+ok", "disable_web_page_preview=true", "parse_mode=HTML"} {
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

func TestTelegramNotifyDocumentUploadsMultipartFile(t *testing.T) {
	var gotPath, gotChat, gotCaption, gotMode, gotName, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotChat, gotCaption, gotMode = r.FormValue("chat_id"), r.FormValue("caption"), r.FormValue("parse_mode")
		f, h, err := r.FormFile("document")
		if err != nil {
			t.Errorf("document field: %v", err)
		} else {
			b, _ := io.ReadAll(f)
			gotName, gotFile = h.Filename, string(b)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := NewTelegram("123:abc", "-4242").(*telegram)
	n.baseURL = srv.URL

	if err := n.NotifyDocument(context.Background(), "triage-R1.html", []byte("<html>hi</html>"), "🧪 review"); err != nil {
		t.Fatalf("NotifyDocument: %v", err)
	}
	if gotPath != "/bot123:abc/sendDocument" || gotChat != "-4242" || gotCaption != "🧪 review" || gotMode != "HTML" {
		t.Errorf("path=%q chat=%q caption=%q mode=%q", gotPath, gotChat, gotCaption, gotMode)
	}
	if gotName != "triage-R1.html" || gotFile != "<html>hi</html>" {
		t.Errorf("file %q = %q", gotName, gotFile)
	}
}

func TestTelegramNotifyDocumentRedactsTokenOnTransportError(t *testing.T) {
	n := NewTelegram("123:secret", "-1").(*telegram)
	n.baseURL = "http://127.0.0.1:1"
	err := n.NotifyDocument(context.Background(), "f.html", []byte("x"), "")
	if err == nil || strings.Contains(err.Error(), "123:secret") {
		t.Fatalf("err = %v", err)
	}
}
