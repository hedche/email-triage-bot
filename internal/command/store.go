package command

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Store loads and saves the Overlay State. version is an opaque token from
// Load that Save passes back so a concurrent writer is detected, not
// overwritten.
type Store interface {
	Load(ctx context.Context) (st *State, version string, err error)
	Save(ctx context.Context, st *State, version string) error
}

// FileStore keeps the State in a local YAML file, for local runs and tests.
type FileStore struct{ Path string }

func (f FileStore) Load(context.Context) (*State, string, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{}, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read overlay: %w", err)
	}
	st := &State{}
	if err := yaml.Unmarshal(data, st); err != nil {
		return nil, "", fmt.Errorf("parse overlay %s: %w", f.Path, err)
	}
	return st, "", nil
}

func (f FileStore) Save(_ context.Context, st *State, _ string) error {
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write overlay: %w", err)
	}
	return os.Rename(tmp, f.Path)
}

// SecretKey is the key of the Overlay document inside the Secret, and so the
// file name it is mounted as.
const SecretKey = "overlay.yaml"

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// SecretStore keeps the State in one key of a Kubernetes Secret, using the
// pod's service account. The Secret must already exist: the bot is granted
// get and update on that one Secret by name and nothing else.
type SecretStore struct {
	Namespace string
	Name      string

	host   string
	token  string
	client *http.Client
}

// NewSecretStore builds a SecretStore from the in-cluster service account.
func NewSecretStore(name string) (*SecretStore, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("overlay secret: not running in a cluster (KUBERNETES_SERVICE_HOST unset)")
	}
	ns, err := os.ReadFile(saDir + "/namespace")
	if err != nil {
		return nil, fmt.Errorf("overlay secret: %w", err)
	}
	token, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, fmt.Errorf("overlay secret: %w", err)
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("overlay secret: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("overlay secret: no certificates in ca.crt")
	}
	return &SecretStore{
		Namespace: strings.TrimSpace(string(ns)),
		Name:      name,
		host:      "https://" + host + ":" + port,
		token:     strings.TrimSpace(string(token)),
		client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
	}, nil
}

type secretObject struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   json.RawMessage   `json:"metadata"`
	Type       string            `json:"type,omitempty"`
	Data       map[string][]byte `json:"data,omitempty"`
}

type secretMeta struct {
	ResourceVersion string `json:"resourceVersion"`
}

func (s *SecretStore) url() string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/secrets/%s", s.host, s.Namespace, s.Name)
}

func (s *SecretStore) Load(ctx context.Context) (*State, string, error) {
	obj, err := s.get(ctx)
	if err != nil {
		return nil, "", err
	}
	var meta secretMeta
	if err := json.Unmarshal(obj.Metadata, &meta); err != nil {
		return nil, "", fmt.Errorf("overlay secret metadata: %w", err)
	}
	st := &State{}
	if raw := obj.Data[SecretKey]; len(raw) > 0 {
		if err := yaml.Unmarshal(raw, st); err != nil {
			return nil, "", fmt.Errorf("parse overlay secret %s/%s: %w", s.Namespace, s.Name, err)
		}
	}
	return st, meta.ResourceVersion, nil
}

// Save replaces the Secret's overlay key. The resourceVersion from Load makes
// the API server refuse the write if anything changed the Secret in between.
func (s *SecretStore) Save(ctx context.Context, st *State, version string) error {
	obj, err := s.get(ctx)
	if err != nil {
		return err
	}
	var meta map[string]any
	if err := json.Unmarshal(obj.Metadata, &meta); err != nil {
		return fmt.Errorf("overlay secret metadata: %w", err)
	}
	if current, _ := meta["resourceVersion"].(string); current != version {
		return fmt.Errorf("overlay secret changed since it was read (version %s, now %s)", version, current)
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	if obj.Data == nil {
		obj.Data = map[string][]byte{}
	}
	obj.Data[SecretKey] = data
	if obj.Metadata, err = json.Marshal(meta); err != nil {
		return err
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = s.do(ctx, http.MethodPut, body)
	return err
}

func (s *SecretStore) get(ctx context.Context) (*secretObject, error) {
	body, err := s.do(ctx, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	obj := &secretObject{}
	if err := json.Unmarshal(body, obj); err != nil {
		return nil, fmt.Errorf("overlay secret: %w", err)
	}
	return obj, nil
}

func (s *SecretStore) do(ctx context.Context, method string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.url(), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("overlay secret %s: %w", method, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The API's error Status names the Secret and the reason; it never
		// echoes the data.
		return nil, fmt.Errorf("overlay secret %s %s/%s: status %d: %s",
			method, s.Namespace, s.Name, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}
