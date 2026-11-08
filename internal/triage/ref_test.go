package triage

import "testing"

func TestRefIsStableAndPerMailbox(t *testing.T) {
	a := Message{ID: "g1", Account: "Me@Example.com", RFCMessageID: " <abc@mail.example> "}
	same := Message{ID: "g-different", Account: "me@example.com", RFCMessageID: "<abc@mail.example>"}
	other := Message{ID: "g1", Account: "you@example.com", RFCMessageID: "<abc@mail.example>"}

	if a.Ref() != same.Ref() {
		t.Errorf("same email, different refs: %s %s", a.Ref(), same.Ref())
	}
	if a.Ref() == other.Ref() {
		t.Error("the same email in another mailbox should get its own ref")
	}
	if len(a.Ref()) != 12 {
		t.Errorf("ref %q", a.Ref())
	}
	noID := Message{ID: "g1", Account: "me@example.com"}
	if noID.Ref() == "" || noID.Ref() == (Message{ID: "g2", Account: "me@example.com"}).Ref() {
		t.Error("a message with no Message-ID should fall back to its provider ID")
	}
}
