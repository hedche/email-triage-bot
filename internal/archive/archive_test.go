package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewNeedsABucket(t *testing.T) {
	if New(Config{}) != nil {
		t.Error("no configuration should mean no store")
	}
}

func TestPutSignsAndUploads(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")

	s := New(Config{Host: host, Port: port, Bucket: "b", AccessKey: "AK", SecretKey: "SK"}).(*S3)
	s.now = func() time.Time { return time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC) }
	if err := s.Put(context.Background(), "runs/2026/11/08/r1.jsonl.gz", []byte("data"), "application/gzip"); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/b/runs/2026/11/08/r1.jsonl.gz" || string(body) != "data" {
		t.Errorf("path %q body %q", got.URL.Path, body)
	}
	auth := got.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AK/20261108/us-east-1/s3/aws4_request,") || !strings.Contains(auth, "Signature=") {
		t.Errorf("authorization %q", auth)
	}
	if strings.Contains(auth, "SK") {
		t.Error("the secret key leaked into a header")
	}
}

func TestPutReportsAnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "AccessDenied", http.StatusForbidden)
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	err := New(Config{Host: host, Port: port, Bucket: "b", AccessKey: "AK", SecretKey: "SK"}).Put(context.Background(), "k", nil, "text/plain")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v", err)
	}
}

func TestGzipJSONL(t *testing.T) {
	raw, err := GzipJSONL([]map[string]int{{"a": 1}, {"b": 2}})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	if string(out) != "{\"a\":1}\n{\"b\":2}\n" {
		t.Errorf("got %q", out)
	}
}
