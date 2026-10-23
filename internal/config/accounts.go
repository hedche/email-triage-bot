package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderGmail is the only Provider today, and the default in accounts.yaml.
const ProviderGmail = "gmail"

// Account is one mailbox the bot is authorised to act on.
type Account struct {
	Email        string `yaml:"email" json:"email"`
	RefreshToken string `yaml:"refresh_token" json:"-"`
	Provider     string `yaml:"provider,omitempty" json:"provider,omitempty"`
}

type accountsFile struct {
	Accounts []Account `yaml:"accounts"`
}

// LoadAccounts reads the accounts file. Emails are trimmed and lower-cased,
// Provider defaults to gmail, and a missing refresh token or a repeated
// Account is an error.
func LoadAccounts(path string) ([]Account, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read accounts file: %w", err)
	}
	var f accountsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse accounts file %s: %w", path, err)
	}

	accounts := make([]Account, 0, len(f.Accounts))
	seen := make(map[string]bool, len(f.Accounts))
	for i, a := range f.Accounts {
		a.Email = strings.ToLower(strings.TrimSpace(a.Email))
		a.RefreshToken = strings.TrimSpace(a.RefreshToken)
		a.Provider = strings.ToLower(strings.TrimSpace(a.Provider))
		if a.Email == "" {
			return nil, fmt.Errorf("accounts[%d]: email must not be empty", i)
		}
		if a.RefreshToken == "" {
			return nil, fmt.Errorf("accounts[%d] (%s): refresh_token must not be empty", i, a.Email)
		}
		if a.Provider == "" {
			a.Provider = ProviderGmail
		}
		if seen[a.Email] {
			return nil, fmt.Errorf("accounts[%d]: duplicate account %q", i, a.Email)
		}
		seen[a.Email] = true
		accounts = append(accounts, a)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("accounts file %s: no accounts configured", path)
	}
	return accounts, nil
}
