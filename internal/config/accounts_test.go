package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAccounts(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
	return path
}

func TestLoadAccountsFromTestdata(t *testing.T) {
	accounts, err := LoadAccounts(filepath.Join("testdata", "accounts.yaml"))
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	want := []Account{
		{Email: "someone@gmail.com", RefreshToken: "1//aaaa", Provider: ProviderGmail},
		{Email: "other@example.com", RefreshToken: "1//bbbb", Provider: ProviderGmail},
	}
	if len(accounts) != len(want) {
		t.Fatalf("got %d accounts, want %d", len(accounts), len(want))
	}
	for i := range want {
		if accounts[i] != want[i] {
			t.Errorf("accounts[%d] = %+v, want %+v", i, accounts[i], want[i])
		}
	}
}

func TestLoadAccountsErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "missing refresh token",
			body:    "accounts:\n  - email: a@example.com\n",
			wantErr: "refresh_token must not be empty",
		},
		{
			name:    "blank refresh token",
			body:    "accounts:\n  - email: a@example.com\n    refresh_token: \"   \"\n",
			wantErr: "refresh_token must not be empty",
		},
		{
			name:    "missing email",
			body:    "accounts:\n  - refresh_token: \"1//x\"\n",
			wantErr: "email must not be empty",
		},
		{
			name:    "duplicate account differing only in case",
			body:    "accounts:\n  - email: A@Example.com\n    refresh_token: \"1//x\"\n  - email: a@example.com\n    refresh_token: \"1//y\"\n",
			wantErr: "duplicate account",
		},
		{
			name:    "no accounts",
			body:    "accounts: []\n",
			wantErr: "no accounts configured",
		},
		{
			name:    "malformed yaml",
			body:    "accounts: [oops\n",
			wantErr: "parse accounts file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadAccounts(writeAccounts(t, tt.body))
			requireErr(t, err, tt.wantErr)
		})
	}
}

func TestLoadAccountsMissingFile(t *testing.T) {
	_, err := LoadAccounts(filepath.Join(t.TempDir(), "nope.yaml"))
	requireErr(t, err, "read accounts file")
}

func TestLoadAccountsKeepsAnExplicitProvider(t *testing.T) {
	accounts, err := LoadAccounts(writeAccounts(t,
		"accounts:\n  - email: a@example.com\n    refresh_token: \"1//x\"\n    provider: imap\n"))
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	if accounts[0].Provider != "imap" {
		t.Errorf("provider = %q, want %q", accounts[0].Provider, "imap")
	}
}

func TestAccountRefreshTokenNeverSerialisesToJSON(t *testing.T) {
	// The refresh token is a credential: it must not leak into logs or files
	// that marshal an Account.
	a := Account{Email: "a@example.com", RefreshToken: "1//secret", Provider: ProviderGmail}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "1//secret") {
		t.Errorf("refresh token leaked into JSON: %s", data)
	}
}
