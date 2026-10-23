package gmail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
)

// authTimeout bounds how long Authorize waits for a person to finish the
// consent screen before giving up and releasing the listener.
const authTimeout = 5 * time.Minute

// callbackPath is the loopback path Google redirects back to. Desktop OAuth
// clients may use any 127.0.0.1 port and path, so a fresh ephemeral port per
// run avoids clashing with anything already listening.
const callbackPath = "/oauth2/callback"

// Authorize runs the local OAuth consent flow and returns the refresh token for
// whichever account the person signs in as. It prints the consent URL to out
// and tries to open a browser, but never prints the token: the caller decides
// how to present a credential.
func Authorize(ctx context.Context, clientID, clientSecret string, out io.Writer) (string, error) {
	if clientID == "" || clientSecret == "" {
		return "", errors.New("gmail: authorize needs both a client id and a client secret")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("gmail: listening for the OAuth callback: %w", err)
	}
	defer ln.Close()

	redirect := fmt.Sprintf("http://%s%s", ln.Addr().String(), callbackPath)
	cfg := oauthConfig(clientID, clientSecret, redirect)

	state, err := randomState()
	if err != nil {
		return "", err
	}

	// access_type=offline asks for a refresh token; prompt=consent forces the
	// consent screen even for an already-approved client, because Google issues
	// a refresh token only on the first approval otherwise.
	consentURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("gmail: OAuth callback state did not match; ignoring the response")
		case q.Get("error") != "":
			res.err = fmt.Errorf("gmail: consent was refused: %s", q.Get("error"))
		case q.Get("code") == "":
			res.err = errors.New("gmail: OAuth callback carried no authorisation code")
		default:
			res.code = q.Get("code")
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, "<html><body><h1>Authorisation failed</h1><p>Return to the terminal.</p></body></html>")
		} else {
			io.WriteString(w, "<html><body><h1>Authorised</h1><p>You can close this tab and return to the terminal.</p></body></html>")
		}

		select {
		case results <- res:
		default: // already answered; a refresh of the callback URL changes nothing
		}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(out, "Open this URL to authorise the bot:\n\n%s\n\n", consentURL)
	fmt.Fprintf(out, "Waiting for the callback on %s ...\n", redirect)
	openBrowser(consentURL)

	timeout := time.NewTimer(authTimeout)
	defer timeout.Stop()

	var res result
	select {
	case res = <-results:
	case <-ctx.Done():
		return "", fmt.Errorf("gmail: authorisation cancelled: %w", ctx.Err())
	case <-timeout.C:
		return "", fmt.Errorf("gmail: authorisation timed out after %s", authTimeout)
	}
	if res.err != nil {
		return "", res.err
	}

	tok, err := cfg.Exchange(ctx, res.code)
	if err != nil {
		return "", fmt.Errorf("gmail: exchanging the authorisation code: %w", err)
	}
	if tok.RefreshToken == "" {
		return "", errors.New("gmail: Google returned no refresh token; revoke the app's access and try again")
	}
	return tok.RefreshToken, nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gmail: generating the OAuth state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// openBrowser is a convenience only: the URL has already been printed, so a
// headless machine or a missing opener is not a failure.
func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		return // start is a shell builtin; the printed URL is enough
	default:
		cmd = "xdg-open"
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return
	}
	_ = exec.Command(path, url).Start()
}
