package gmail

import (
	"context"
	"fmt"
	"strings"

	"github.com/hedche/email-triage-bot/internal/mail"
	gmailapi "google.golang.org/api/gmail/v1"
)

var _ mail.Provider = (*Provider)(nil)

// ListUnread returns the IDs of unread inbox messages matching q, newest first
// (Gmail's own ordering, which the search endpoint guarantees).
//
// The excluded labels are filtered twice, deliberately. The search query does
// carry a `-label:"Triage/Read"` term, which keeps the pages small, but Gmail's
// search syntax for labels containing a "/" is unreliable: a nested label is
// matched by its leaf name in some mailboxes and by its full path in others,
// and a name that needs quoting can be silently dropped. So the query is an
// optimisation only, and the authoritative filter is the client-side one below,
// which compares real label IDs from a cheap metadata fetch. Getting this wrong
// in the lenient direction means re-judging messages a previous run already
// decided; getting it wrong the other way means missing mail entirely.
func (p *Provider) ListUnread(ctx context.Context, q mail.Query) ([]string, error) {
	ids, err := p.searchUnread(ctx, q)
	if err != nil {
		return nil, err
	}

	excluded, err := p.excludedLabelIDs(ctx, q.ExcludeLabels)
	if err != nil {
		return nil, err
	}
	if len(excluded) == 0 {
		// Nothing to exclude, or no excluded label exists in this mailbox yet
		// (a first run): skip a per-message round trip each.
		return ids, nil
	}

	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		var msg *gmailapi.Message
		err := p.do(ctx, "messages.get(minimal)", id, func() error {
			var gerr error
			msg, gerr = p.svc.Users.Messages.Get(userID, id).Format("minimal").Context(ctx).Do()
			return gerr
		})
		if err != nil {
			return nil, err
		}
		if !hasAny(msg.LabelIds, excluded) {
			kept = append(kept, id)
		}
	}
	return kept, nil
}

// searchUnread pages users.messages.list until q.Max ids are collected or Gmail
// runs out of results.
func (p *Provider) searchUnread(ctx context.Context, q mail.Query) ([]string, error) {
	query := buildQuery(q)

	var ids []string
	pageToken := ""
	for {
		pageSize := listPageSize
		if q.Max > 0 {
			if remaining := q.Max - len(ids); remaining < pageSize {
				pageSize = remaining
			}
		}
		if pageSize <= 0 {
			break
		}

		var resp *gmailapi.ListMessagesResponse
		call := func() error {
			c := p.svc.Users.Messages.List(userID).Q(query).MaxResults(int64(pageSize))
			if pageToken != "" {
				c = c.PageToken(pageToken)
			}
			var lerr error
			resp, lerr = c.Context(ctx).Do()
			return lerr
		}
		if err := p.do(ctx, "messages.list", "", call); err != nil {
			return nil, fmt.Errorf("query %q: %w", query, err)
		}

		for _, m := range resp.Messages {
			ids = append(ids, m.Id)
			if q.Max > 0 && len(ids) >= q.Max {
				return ids, nil
			}
		}
		if resp.NextPageToken == "" {
			return ids, nil
		}
		pageToken = resp.NextPageToken
	}
	return ids, nil
}

// buildQuery renders the Gmail search for one account's work queue.
func buildQuery(q mail.Query) string {
	terms := []string{"in:inbox", "is:unread"}
	if q.LookbackDays > 0 {
		terms = append(terms, fmt.Sprintf("newer_than:%dd", q.LookbackDays))
	}
	for _, name := range q.ExcludeLabels {
		if name == "" {
			continue
		}
		terms = append(terms, fmt.Sprintf(`-label:%q`, name))
	}
	return strings.Join(terms, " ")
}

// excludedLabelIDs resolves label names to IDs, skipping names the mailbox does
// not have (they cannot be on any message, so they exclude nothing).
func (p *Provider) excludedLabelIDs(ctx context.Context, names []string) (map[string]bool, error) {
	wanted := make([]string, 0, len(names))
	for _, n := range names {
		if n != "" {
			wanted = append(wanted, n)
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	labels, err := p.listLabels(ctx, false)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(wanted))
	for _, name := range wanted {
		if id, ok := lookupLabel(labels, name); ok {
			ids[id] = true
		}
	}
	return ids, nil
}

func hasAny(have []string, want map[string]bool) bool {
	for _, h := range have {
		if want[h] {
			return true
		}
	}
	return false
}
