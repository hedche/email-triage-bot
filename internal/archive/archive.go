// Package archive keeps a durable record of what the bot did, in an
// S3-compatible bucket: one compressed file per Triage Run and one file per
// batch of Email Command events. Archiving is best-effort: a failure is logged
// and reported, and never fails the work it was recording.
package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Store writes one object. Keys are plain paths using only [A-Za-z0-9/._-].
type Store interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
}

// ReadStore is a Store that can also read an object back. Only the backfill
// needs it: no Triage Run reads the archive (ADR 0006).
type ReadStore interface {
	Store
	// Get returns an object's bytes, or ErrNotFound when the key is empty.
	Get(ctx context.Context, key string) ([]byte, error)
}

// ErrNotFound is what Get returns for a key that holds no object.
var ErrNotFound = errors.New("archive: object not found")

// Config locates a bucket. The field names mirror what a Rook
// ObjectBucketClaim publishes (BUCKET_HOST, BUCKET_PORT, BUCKET_NAME,
// AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY).
type Config struct {
	Host      string
	Port      string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string // defaults to us-east-1, which Ceph RGW accepts
}

// S3 is a Store for one bucket, using path-style URLs and Signature V4. It
// does only single-object PUTs, which is why it is this small and not an SDK.
type S3 struct {
	cfg    Config
	client *http.Client
	now    func() time.Time
}

// New returns a Store for cfg, or nil when no bucket is configured.
func New(cfg Config) Store {
	if cfg.Bucket == "" || cfg.Host == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return &S3{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
}

func (s *S3) endpoint() (scheme, host string) {
	scheme, host = "http", s.cfg.Host
	switch s.cfg.Port {
	case "", "80":
	case "443":
		scheme = "https"
	default:
		host += ":" + s.cfg.Port
	}
	return scheme, host
}

// Put uploads body to key.
func (s *S3) Put(ctx context.Context, key string, body []byte, contentType string) error {
	scheme, host := s.endpoint()
	path := "/" + s.cfg.Bucket + "/" + strings.TrimPrefix(key, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, scheme+"://"+host+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("archive %s: %w", key, err)
	}
	s.sign(req, host, path, body, contentType)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("archive %s: %w", key, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("archive %s: status %d: %s", key, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Get downloads key, returning ErrNotFound when no object is there.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	scheme, host := s.endpoint()
	path := "/" + s.cfg.Bucket + "/" + strings.TrimPrefix(key, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+path, nil)
	if err != nil {
		return nil, fmt.Errorf("archive %s: %w", key, err)
	}
	s.sign(req, host, path, nil, "")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("archive %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("archive %s: status %d: %s", key, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("archive %s: %w", key, err)
	}
	return body, nil
}

// sign adds the AWS Signature V4 headers for a request with no query string.
// contentType is empty for a GET, which has no body to describe.
func (s *S3) sign(req *http.Request, host, path string, body []byte, contentType string) {
	now := s.now().UTC()
	amzDate, day := now.Format("20060102T150405Z"), now.Format("20060102")
	payload := sha256Hex(body)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Amz-Content-Sha256", payload)
	req.Header.Set("X-Amz-Date", amzDate)

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	headerLines := []string{
		"host:" + host,
		"x-amz-content-sha256:" + payload,
		"x-amz-date:" + amzDate,
	}
	if contentType != "" {
		signedHeaders = "content-type;" + signedHeaders
		headerLines = append([]string{"content-type:" + contentType}, headerLines...)
	}
	canonical := strings.Join(append(append([]string{req.Method, path, ""}, headerLines...), "", signedHeaders, payload), "\n")
	scope := day + "/" + s.cfg.Region + "/s3/aws4_request"
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonical))}, "\n")

	k := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), day)
	k = hmacSHA256(k, s.cfg.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signedHeaders, hex.EncodeToString(hmacSHA256(k, toSign))))
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// GzipJSONL encodes each element of items as one JSON line and compresses the
// result. Decompressed, a run's file is a Decision Log that replay can read.
func GzipJSONL[T any](items []T) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RunKey is where one Triage Run's records go.
func RunKey(at time.Time, runID string) string {
	return "runs/" + at.UTC().Format("2006/01/02") + "/" + runID + ".jsonl.gz"
}

// RunSummaryKey is where one Triage Run's summary goes, beside its records.
func RunSummaryKey(at time.Time, runID string) string {
	return "runs/" + at.UTC().Format("2006/01/02") + "/" + runID + ".summary.json"
}

// CommandsKey is where one batch of Email Command events goes.
func CommandsKey(at time.Time, id string) string {
	return "commands/" + at.UTC().Format("2006/01/02") + "/" + at.UTC().Format("150405") + "-" + id + ".json"
}
