package gmail

import (
	"context"

	gmailapi "google.golang.org/api/gmail/v1"
)

// unreadLabel is Gmail's system label for the unread state; removing it is what
// "mark read" means.
const unreadLabel = "UNREAD"

// MarkRead clears the unread state of ids and applies labelID (the Read Audit
// Label). batchModify is idempotent, so a retried or repeated run is harmless.
func (p *Provider) MarkRead(ctx context.Context, ids []string, labelID string) error {
	add := []string(nil)
	if labelID != "" {
		add = []string{labelID}
	}
	return p.batchModify(ctx, "messages.batchModify(markRead)", ids, add, []string{unreadLabel})
}

// Label applies labelID to ids without touching their unread state.
func (p *Provider) Label(ctx context.Context, ids []string, labelID string) error {
	if labelID == "" {
		return nil
	}
	return p.batchModify(ctx, "messages.batchModify(label)", ids, []string{labelID}, nil)
}

func (p *Provider) batchModify(ctx context.Context, op string, ids, add, remove []string) error {
	for _, batch := range chunk(ids, batchModifyLimit) {
		req := &gmailapi.BatchModifyMessagesRequest{
			Ids:            batch,
			AddLabelIds:    add,
			RemoveLabelIds: remove,
		}
		err := p.do(ctx, op, "", func() error {
			return p.svc.Users.Messages.BatchModify(userID, req).Context(ctx).Do()
		})
		if err != nil {
			return err
		}
	}
	return nil
}
