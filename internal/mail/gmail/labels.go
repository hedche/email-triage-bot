package gmail

import (
	"context"
	"fmt"
	"strings"

	gmailapi "google.golang.org/api/gmail/v1"
)

// EnsureLabels creates any of names that the mailbox does not already have and
// returns name -> Gmail label ID for all of them. Gmail treats "Triage/Read" as
// a nested label automatically; no parent has to be created first.
func (p *Provider) EnsureLabels(ctx context.Context, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}

	existing, err := p.listLabels(ctx, true)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(names))
	created := false
	for _, name := range names {
		if name == "" {
			continue
		}
		if id, ok := lookupLabel(existing, name); ok {
			out[name] = id
			continue
		}
		var lbl *gmailapi.Label
		err := p.do(ctx, "labels.create", "", func() error {
			var cerr error
			lbl, cerr = p.svc.Users.Labels.Create(userID, &gmailapi.Label{
				Name:                  name,
				LabelListVisibility:   "labelShow",
				MessageListVisibility: "show",
			}).Context(ctx).Do()
			return cerr
		})
		if err != nil {
			return nil, fmt.Errorf("creating label %q: %w", name, err)
		}
		existing[name] = lbl.Id
		out[name] = lbl.Id
		created = true
	}

	if created {
		p.mu.Lock()
		p.labelIDs = existing
		p.mu.Unlock()
	}
	return out, nil
}

// listLabels returns the mailbox's label name -> ID map. It is fetched once and
// cached on the Provider: a run asks for the same handful of Audit Labels for
// every message it considers, and a mailbox's label set does not change
// underneath a single run in any way that matters. Pass refresh to force a
// fresh call (EnsureLabels does, so it never reports a stale "missing").
func (p *Provider) listLabels(ctx context.Context, refresh bool) (map[string]string, error) {
	if !refresh {
		p.mu.Lock()
		cached := p.labelIDs
		p.mu.Unlock()
		if cached != nil {
			return cached, nil
		}
	}

	var resp *gmailapi.ListLabelsResponse
	err := p.do(ctx, "labels.list", "", func() error {
		var lerr error
		resp, lerr = p.svc.Users.Labels.List(userID).Context(ctx).Do()
		return lerr
	})
	if err != nil {
		return nil, err
	}

	labels := make(map[string]string, len(resp.Labels))
	for _, l := range resp.Labels {
		labels[l.Name] = l.Id
	}

	p.mu.Lock()
	p.labelIDs = labels
	p.mu.Unlock()
	return labels, nil
}

// lookupLabel finds a label by name, preferring an exact match but falling back
// to a case-insensitive one. Gmail rejects creating a label whose name differs
// from an existing one only by case, so treating "triage/read" as a miss would
// make EnsureLabels fail rather than reuse what is already there.
func lookupLabel(labels map[string]string, name string) (string, bool) {
	if id, ok := labels[name]; ok {
		return id, true
	}
	for existing, id := range labels {
		if strings.EqualFold(existing, name) {
			return id, true
		}
	}
	return "", false
}
