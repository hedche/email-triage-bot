package gmail

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
)

// fakeGmail is just enough of the Gmail REST API for the provider to drive:
// messages.list (with paging), messages.get, labels.list, labels.create and
// messages.batchModify. It records every request so a test can assert on the
// query string and on how calls were chunked.
type fakeGmail struct {
	t *testing.T

	mu sync.Mutex

	// pages are returned by messages.list in order, one per call.
	pages []gmailapi.ListMessagesResponse
	// messages is keyed by id; the same message answers both the minimal and
	// the full form, the handler trims it to match the requested format.
	messages map[string]*gmailapi.Message
	labels   []*gmailapi.Label

	// failures[path] is a countdown of responses to fail with failStatus before
	// the handler answers normally.
	failures   map[string]int
	failStatus int

	// recorded traffic
	listQueries  []string
	listPageToks []string
	getFormats   map[string]string
	getCalls     []string
	batchModify  []gmailapi.BatchModifyMessagesRequest
	createdNames []string
	nextLabelID  int
}

func newFakeGmail(t *testing.T) *fakeGmail {
	return &fakeGmail{
		t:           t,
		messages:    map[string]*gmailapi.Message{},
		failures:    map[string]int{},
		failStatus:  http.StatusTooManyRequests,
		getFormats:  map[string]string{},
		nextLabelID: 1,
	}
}

// start wires the fake into an httptest.Server and returns a Provider pointed
// at it with near-zero retry delays.
func (f *fakeGmail) start() (*Provider, *httptest.Server) {
	srv := httptest.NewServer(f)
	f.t.Cleanup(srv.Close)

	p, err := newForTest("someone@gmail.com", srv.URL+"/", srv.Client())
	if err != nil {
		f.t.Fatalf("newForTest: %v", err)
	}
	p.backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return p, srv
}

func (f *fakeGmail) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me")
	if n := f.failures[path]; n > 0 {
		f.failures[path] = n - 1
		http.Error(w, `{"error":{"code":`+strconv.Itoa(f.failStatus)+`,"message":"injected"}}`, f.failStatus)
		return
	}

	switch {
	case path == "/messages" && r.Method == http.MethodGet:
		f.handleList(w, r)
	case strings.HasPrefix(path, "/messages/") && r.Method == http.MethodGet:
		f.handleGet(w, r, strings.TrimPrefix(path, "/messages/"))
	case path == "/messages/batchModify" && r.Method == http.MethodPost:
		f.handleBatchModify(w, r)
	case path == "/labels" && r.Method == http.MethodGet:
		writeJSON(w, gmailapi.ListLabelsResponse{Labels: f.labels})
	case path == "/labels" && r.Method == http.MethodPost:
		f.handleCreateLabel(w, r)
	default:
		f.t.Errorf("fake gmail: unexpected %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

func (f *fakeGmail) handleList(w http.ResponseWriter, r *http.Request) {
	f.listQueries = append(f.listQueries, r.URL.Query().Get("q"))
	f.listPageToks = append(f.listPageToks, r.URL.Query().Get("pageToken"))

	idx := len(f.listQueries) - 1
	if idx >= len(f.pages) {
		writeJSON(w, gmailapi.ListMessagesResponse{})
		return
	}
	page := f.pages[idx]

	// Honour maxResults the way Gmail does: never return more than asked.
	if maxStr := r.URL.Query().Get("maxResults"); maxStr != "" {
		if max, err := strconv.Atoi(maxStr); err == nil && max < len(page.Messages) {
			page.Messages = page.Messages[:max]
		}
	}
	writeJSON(w, page)
}

func (f *fakeGmail) handleGet(w http.ResponseWriter, r *http.Request, id string) {
	format := r.URL.Query().Get("format")
	f.getFormats[id] = format
	f.getCalls = append(f.getCalls, id)

	msg, ok := f.messages[id]
	if !ok {
		http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
		return
	}
	out := *msg
	if format == "minimal" {
		out.Payload = nil // Gmail omits the payload for the minimal form
	}
	writeJSON(w, out)
}

func (f *fakeGmail) handleBatchModify(w http.ResponseWriter, r *http.Request) {
	var req gmailapi.BatchModifyMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("batchModify: decoding body: %v", err)
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	f.batchModify = append(f.batchModify, req)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeGmail) handleCreateLabel(w http.ResponseWriter, r *http.Request) {
	var lbl gmailapi.Label
	if err := json.NewDecoder(r.Body).Decode(&lbl); err != nil {
		f.t.Errorf("labels.create: decoding body: %v", err)
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	lbl.Id = "Label_" + strconv.Itoa(f.nextLabelID)
	f.nextLabelID++
	f.createdNames = append(f.createdNames, lbl.Name)
	f.labels = append(f.labels, &lbl)
	writeJSON(w, lbl)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

// --- helpers for building fixtures ------------------------------------------

func (f *fakeGmail) addMessage(m *gmailapi.Message) {
	f.messages[m.Id] = m
}

func (f *fakeGmail) addLabel(id, name string) {
	f.labels = append(f.labels, &gmailapi.Label{Id: id, Name: name})
}

func (f *fakeGmail) page(nextToken string, ids ...string) {
	page := gmailapi.ListMessagesResponse{NextPageToken: nextToken}
	for _, id := range ids {
		page.Messages = append(page.Messages, &gmailapi.Message{Id: id})
	}
	f.pages = append(f.pages, page)
}
