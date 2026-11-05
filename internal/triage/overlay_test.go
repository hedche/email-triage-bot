package triage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangeValidate(t *testing.T) {
	cases := []struct {
		c  Change
		ok bool
	}{
		{Change{Op: OpReadSender, Value: "news@shop.example"}, true},
		{Change{Op: OpReadSender, Value: "Shop <news@shop.example>"}, false},
		{Change{Op: OpReadDomain, Value: "shop.example"}, true},
		{Change{Op: OpReadDomain, Value: "shop"}, false},
		{Change{Op: OpProtectKeyword, Value: "re"}, false},
		{Change{Op: OpProtectKeyword, Value: "invoice"}, true},
		{Change{Op: OpBrandExample, Brand: "Shop", Note: "dispatch notice", Verdict: VerdictMarkRead}, true},
		{Change{Op: OpBrandExample, Brand: "Shop", Note: "dispatch notice"}, false},
		{Change{Op: "delete_everything", Value: "x"}, false},
	}
	for _, tc := range cases {
		err := tc.c.Normalise().Validate()
		if (err == nil) != tc.ok {
			t.Errorf("%+v: err=%v, want ok=%v", tc.c, err, tc.ok)
		}
	}
}

func TestProtective(t *testing.T) {
	if !(Change{Op: OpProtectDomain}).Protective() {
		t.Error("protect_domain should be protective")
	}
	if (Change{Op: OpReadDomain}).Protective() {
		t.Error("read_domain must not be protective")
	}
	if !(Change{Op: OpBrandExample, Verdict: VerdictLeave}).Protective() {
		t.Error("a leave example should be protective")
	}
	if (Change{Op: OpBrandNote}).Protective() {
		t.Error("a brand note can loosen the Judge and must not be protective")
	}
}

func TestWithOverlay(t *testing.T) {
	base := &Rules{
		Threshold: 0.9,
		RuleSet:   RuleSet{Brands: []BrandRule{{Name: "Shop", Note: "orders are routine"}}},
	}
	got := base.WithOverlay([]Change{
		{Op: OpReadDomain, Value: "news.example"},
		{Op: OpProtectSender, Value: "boss@work.example", Account: "me@example.com"},
		{Op: OpBrandExample, Brand: "shop", Note: "refund issued", Verdict: VerdictLeave, Account: "me@example.com"},
		{Op: OpBrandNote, Brand: "Energy Co", Note: "statements are routine"},
	})

	if got.Threshold != 0.9 {
		t.Errorf("threshold = %v", got.Threshold)
	}
	if len(base.Senders) != 0 || len(base.Brands[0].Examples) != 0 {
		t.Fatal("WithOverlay modified the base rules")
	}
	rs := got.For("me@example.com")
	if len(rs.Senders) != 1 || rs.Senders[0].Domain != "news.example" {
		t.Errorf("senders = %+v", rs.Senders)
	}
	if len(rs.Protect) != 1 || rs.Protect[0].Sender != "boss@work.example" {
		t.Errorf("protect = %+v", rs.Protect)
	}
	if len(rs.Brands) != 2 {
		t.Fatalf("brands = %+v", rs.Brands)
	}
	if rs.Brands[0].Name != "Shop" || len(rs.Brands[0].Examples) != 1 {
		t.Errorf("per-account example not merged into the global brand: %+v", rs.Brands[0])
	}
	other := got.For("someone@example.com")
	if len(other.Protect) != 0 || len(other.Brands[0].Examples) != 0 {
		t.Errorf("per-account changes leaked to another account: %+v", other)
	}
}

func TestForMergesPerAccountBrandOfSameName(t *testing.T) {
	r := &Rules{
		RuleSet:  RuleSet{Brands: []BrandRule{{Name: "Example Energy", Note: "statements are routine."}}},
		Accounts: map[string]RuleSet{"a@example.com": {Brands: []BrandRule{{Name: "example energy", Note: "Leave contracts."}}}},
	}
	rs := r.For("a@example.com")
	if len(rs.Brands) != 1 || rs.Brands[0].Note != "statements are routine. Leave contracts." {
		t.Errorf("brands = %+v", rs.Brands)
	}
	if r.Brands[0].Note != "statements are routine." {
		t.Error("For modified the global brand")
	}
}

func TestLoadOverlay(t *testing.T) {
	if got, err := LoadOverlay(filepath.Join(t.TempDir(), "missing.yaml")); err != nil || got != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
	p := filepath.Join(t.TempDir(), "overlay.yaml")
	doc := "changes:\n  - id: a1\n    op: read_domain\n    value: '@News.Example'\npending: []\ntelegram_offset: 5\n"
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOverlay(p)
	if err != nil || len(got) != 1 || got[0].Value != "news.example" {
		t.Fatalf("got %+v, %v", got, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(bad, []byte("changes:\n  - id: b\n    op: read_domain\n    value: nodot\n"), 0o600)
	if _, err := LoadOverlay(bad); err == nil {
		t.Error("an invalid change should fail the load")
	}
}

func TestSenderChangeWithSubject(t *testing.T) {
	c := Change{Op: OpReadSender, Value: "servicing@email.chase.co.uk", Subject: " Your statement is ready "}.Normalise()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Describe(), `whose subject contains "Your statement is ready"`) {
		t.Errorf("describe %q", c.Describe())
	}
	rs := (&Rules{}).WithOverlay([]Change{c}).For("x@example.com")
	if len(rs.Senders) != 1 || rs.Senders[0].Subject != "Your statement is ready" || rs.Senders[0].Address != c.Value {
		t.Errorf("senders %+v", rs.Senders)
	}
	if (Change{Op: OpProtectSender, Value: "a@b.example", Subject: "x"}).Normalise().Subject != "" {
		t.Error("subject must be dropped for ops that do not take it")
	}
}
