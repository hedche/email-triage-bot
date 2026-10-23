// Package gmail implements mail.Provider against the Gmail REST API, plus the
// local OAuth consent flow the `auth` subcommand uses to mint a refresh token.
//
// One Provider is bound to exactly one Account for its lifetime. Every method
// is safe to call sequentially from a single goroutine, which is all the run
// requires; the label cache is nevertheless mutex-guarded so a stray
// concurrent call cannot corrupt it.
package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

const (
	// Scope is the only OAuth scope the bot asks for: it can read messages and
	// change their labels and unread state, and nothing else.
	Scope = "https://www.googleapis.com/auth/gmail.modify"

	authURL  = "https://accounts.google.com/o/oauth2/auth"
	tokenURL = "https://oauth2.googleapis.com/token"

	// userID is the Gmail alias for "the mailbox this token is for". The bot
	// never addresses a mailbox by address; the refresh token picks it.
	userID = "me"

	// batchModifyLimit is the Gmail cap on ids per users.messages.batchModify.
	batchModifyLimit = 1000

	// listPageSize is the page size for users.messages.list. Gmail allows more,
	// but 100 keeps a single page cheap and the cap easy to respect.
	listPageSize = 100
)

// Provider is one authorised Gmail mailbox.
type Provider struct {
	svc     *gmailapi.Service
	account string

	// backoff holds the retry delays; it is a field only so tests can shorten
	// them. Production value is defaultBackoff.
	backoff []time.Duration

	mu sync.Mutex
	// labelIDs caches the mailbox's label name -> ID map. Label sets are small
	// and change rarely, so one labels.list per process is plenty; EnsureLabels
	// refreshes it whenever it creates something.
	labelIDs map[string]string
}

// New builds a Provider for account from a long-lived refresh token. The
// clientID/clientSecret pair is the single OAuth desktop client shared by every
// account; the refresh token is what distinguishes them.
func New(ctx context.Context, clientID, clientSecret, refreshToken, account string) (*Provider, error) {
	switch {
	case clientID == "":
		return nil, errors.New("gmail: client id is empty")
	case clientSecret == "":
		return nil, errors.New("gmail: client secret is empty")
	case refreshToken == "":
		return nil, fmt.Errorf("gmail: refresh token for account %s is empty", account)
	case account == "":
		return nil, errors.New("gmail: account is empty")
	}

	cfg := oauthConfig(clientID, clientSecret, "")
	// A token holding only a refresh token is never Valid, so the first API
	// call exchanges it for an access token and the source caches that.
	ts := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})

	svc, err := gmailapi.NewService(ctx, option.WithTokenSource(ts), option.WithScopes(Scope))
	if err != nil {
		return nil, fmt.Errorf("gmail: building service for account %s: %w", account, err)
	}
	return newProvider(svc, account), nil
}

// newForTest builds a Provider talking to baseURL (which must end in "/")
// through hc, with authentication disabled. It exists so tests can run the
// whole provider against an httptest.Server without credentials.
func newForTest(account, baseURL string, hc *http.Client) (*Provider, error) {
	svc, err := gmailapi.NewService(context.Background(),
		option.WithEndpoint(baseURL),
		option.WithHTTPClient(hc),
		option.WithoutAuthentication(),
	)
	if err != nil {
		return nil, err
	}
	return newProvider(svc, account), nil
}

func newProvider(svc *gmailapi.Service, account string) *Provider {
	return &Provider{svc: svc, account: account, backoff: defaultBackoff}
}

// oauthConfig is the one place the Google OAuth endpoints and scope are named.
// redirectURL is empty for the refresh-token path and set for the consent flow.
func oauthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{Scope},
		Endpoint: oauth2.Endpoint{
			AuthURL:   authURL,
			TokenURL:  tokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// Account returns the mailbox address this Provider is bound to.
func (p *Provider) Account() string { return p.account }
