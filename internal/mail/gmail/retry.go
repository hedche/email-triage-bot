package gmail

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

// defaultBackoff is the delay before each retry: three retries after the
// initial attempt, so four attempts in total.
var defaultBackoff = []time.Duration{
	200 * time.Millisecond,
	800 * time.Millisecond,
	3200 * time.Millisecond,
}

// defaultQuotaBackoff is the delay before each retry once Gmail has said a rate
// limit was hit. The per-user quota is counted per minute, so sub-second retries
// all land in the same spent window. These add up to more than a minute, so the
// last retry always lands in a fresh one. Seen live on 2026-11-11 and 11-12: a
// 403 rateLimitExceeded on "Units per minute per user" failed the whole run.
var defaultQuotaBackoff = []time.Duration{
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
}

// apiError names the failing operation and the mailbox (and message, when the
// call was about one) so a log line from a run across several accounts is
// self-explanatory.
type apiError struct {
	op      string
	account string
	msgID   string
	err     error
}

func (e *apiError) Error() string {
	if e.msgID != "" {
		return fmt.Sprintf("gmail %s: account %s, message %s: %v", e.op, e.account, e.msgID, e.err)
	}
	return fmt.Sprintf("gmail %s: account %s: %v", e.op, e.account, e.err)
}

func (e *apiError) Unwrap() error { return e.err }

// wrap decorates err with the operation and account. It returns nil for a nil
// error so callers can write `return p.wrap(...)` unconditionally.
func (p *Provider) wrap(op, msgID string, err error) error {
	if err == nil {
		return nil
	}
	return &apiError{op: op, account: p.account, msgID: msgID, err: err}
}

// do runs one Gmail call, retrying transient failures (rate limits, 5xx and
// requests that got no answer) with exponential backoff and jitter, and wrapping whatever error survives. The
// call itself closes over its own result; do only reports success or failure.
func (p *Provider) do(ctx context.Context, op, msgID string, call func() error) error {
	delays := p.backoff
	if delays == nil {
		delays = defaultBackoff
	}

	var err error
	for attempt := 0; ; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			if err == nil {
				err = cerr
			}
			return p.wrap(op, msgID, err)
		}
		if err = call(); err == nil {
			return nil
		}
		if rateLimited(err) {
			delays = p.quotaBackoff
			if delays == nil {
				delays = defaultQuotaBackoff
			}
		}
		if attempt >= len(delays) || !retryable(err) {
			return p.wrap(op, msgID, err)
		}
		if werr := wait(ctx, jitter(delays[attempt])); werr != nil {
			return p.wrap(op, msgID, errors.Join(err, werr))
		}
	}
}

// rateLimited reports whether Gmail refused the call for quota. It says so
// with a 429, or with a 403 whose reason is rateLimitExceeded or
// userRateLimitExceeded. A 403 for any other reason is a real refusal.
func rateLimited(err error) bool {
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) {
		return false
	}
	if gerr.Code == http.StatusTooManyRequests {
		return true
	}
	if gerr.Code != http.StatusForbidden {
		return false
	}
	for _, item := range gerr.Errors {
		if item.Reason == "rateLimitExceeded" || item.Reason == "userRateLimitExceeded" {
			return true
		}
	}
	return false
}

// retryable reports whether a failed call is worth another go: the Gmail API
// said "try again" (rate limiting or a server-side fault), or the request never
// got an answer at all (a timeout, a reset connection, a DNS blip). Everything
// the server or the token endpoint actually answered (bad token, bad label,
// 404) is permanent.
func retryable(err error) bool {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return rateLimited(err) || (gerr.Code >= 500 && gerr.Code <= 599)
	}
	// The token exchange runs inside the same request, so a refused refresh
	// token arrives as a *url.Error too. Only a server fault there is transient.
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) {
		return rerr.Response != nil && rerr.Response.StatusCode >= 500
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var uerr *url.Error
	return errors.As(err, &uerr)
}

// jitter spreads retries over +/-20% so a run against several accounts does not
// synchronise its retries into a new burst.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// chunk splits ids into runs of at most size, for the batch endpoints that cap
// how many messages one call may name.
func chunk(ids []string, size int) [][]string {
	if len(ids) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[start:end])
	}
	return out
}
