package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
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

func TestGetReadsAnObjectAndReportsAMissingOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") == "" || r.Header.Get("Content-Type") != "" {
			t.Errorf("unexpected request: %s %v", r.Method, r.Header)
		}
		if strings.HasSuffix(r.URL.Path, "/missing") {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		io.WriteString(w, "data")
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	s := New(Config{Host: host, Port: port, Bucket: "b", AccessKey: "AK", SecretKey: "SK"}).(*S3)

	body, err := s.Get(context.Background(), "runs/there")
	if err != nil || string(body) != "data" {
		t.Fatalf("body %q err %v", body, err)
	}
	if _, err := s.Get(context.Background(), "runs/missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The worked ListObjects example from the AWS Signature V4 documentation.
func TestSignMatchesTheAWSWorkedExampleWithAQuery(t *testing.T) {
	s := New(Config{Host: "h", Bucket: "b", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}).(*S3)
	s.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.sign(req, "examplebucket.s3.amazonaws.com", "/", nil, "")
	const want = "Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"
	if auth := req.Header.Get("Authorization"); !strings.HasSuffix(auth, want) {
		t.Errorf("authorization %q, want it to end %s", auth, want)
	}
}

func TestListFollowsPagination(t *testing.T) {
	var tokens []string
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/b" || r.Header.Get("Authorization") == "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("list-type") != "2" || q.Get("prefix") != "runs/2026/11/08/" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		token := q.Get("continuation-token")
		tokens, queries = append(tokens, token), append(queries, r.URL.RawQuery)
		switch token {
		case "":
			io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>a+b/c=</NextContinuationToken><Contents><Key>k1</Key></Contents><Contents><Key>k2</Key></Contents></ListBucketResult>`)
		case "a+b/c=":
			io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>k3</Key></Contents></ListBucketResult>`)
		default:
			t.Errorf("unexpected token %q", token)
		}
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")

	keys, err := New(Config{Host: host, Port: port, Bucket: "b", AccessKey: "AK", SecretKey: "SK"}).(*S3).List(context.Background(), "runs/2026/11/08/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "k1,k2,k3" || len(tokens) != 2 {
		t.Errorf("keys %v after %d pages", keys, len(tokens))
	}
	want := "continuation-token=a%2Bb%2Fc%3D&list-type=2&prefix=runs%2F2026%2F11%2F08%2F"
	if queries[1] != want {
		t.Errorf("second query %q, want the canonical %q", queries[1], want)
	}
}

func TestListStopsOnATruncatedPageWithNoToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`)
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	_, err := New(Config{Host: host, Port: port, Bucket: "b", AccessKey: "AK", SecretKey: "SK"}).(*S3).List(context.Background(), "p")
	if err == nil || calls != 1 {
		t.Errorf("err = %v after %d calls, want an error after one", err, calls)
	}
}

func TestListReportsAnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "AccessDenied", http.StatusForbidden)
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	_, err := New(Config{Host: host, Port: port, Bucket: "b", AccessKey: "AK", SecretKey: "SK"}).(*S3).List(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v", err)
	}
}

func TestReaderCannotWrite(t *testing.T) {
	if NewReader(Config{}) != nil {
		t.Error("no configuration should mean no reader")
	}
	r := NewReader(Config{Host: "h", Bucket: "b", AccessKey: "AK", SecretKey: "SK"})
	if _, ok := r.(Store); ok {
		t.Error("a Reader must not satisfy Store")
	}
}
