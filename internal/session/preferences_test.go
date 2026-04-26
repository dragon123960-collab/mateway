package session

import "testing"

func TestStorePreferences(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SavePreferences("feishu:p2p:u1", Preferences{AgentName: "writer"}); err != nil {
		t.Fatal(err)
	}
	prefs, err := store.LoadPreferences("feishu:p2p:u1")
	if err != nil {
		t.Fatal(err)
	}
	if prefs.AgentName != "writer" {
		t.Fatalf("unexpected prefs: %#v", prefs)
	}
}
