package store

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *ClientStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	cs, err := NewClientStore(root)
	if err != nil {
		t.Fatalf("NewClientStore: %v", err)
	}
	return cs
}

func TestClientCreateDefaults(t *testing.T) {
	cs := newTestStore(t)
	c, err := cs.Create("acme", "Acme Inc", "notes")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID != "acme" || c.Name != "Acme Inc" || c.Notes != "notes" {
		t.Fatalf("unexpected client: %+v", c)
	}
	if !c.Chargeable {
		t.Error("new clients should default to chargeable")
	}
	if c.PricePerGBHour != DefaultPricePerGBHour {
		t.Errorf("price = %v, want default %v", c.PricePerGBHour, DefaultPricePerGBHour)
	}
	if c.RetentionHours != DefaultRetentionHours {
		t.Errorf("retention = %v, want default %v", c.RetentionHours, DefaultRetentionHours)
	}
}

func TestClientRejectsInvalidIDs(t *testing.T) {
	cs := newTestStore(t)
	for _, id := range []string{"api", "login", "c", "has space", "../etc", "UPPER case", "-lead", "trail-", ""} {
		if _, err := cs.Create(id, id, ""); err == nil {
			t.Errorf("id %q should be rejected", id)
		}
	}
}

func TestClientDuplicate(t *testing.T) {
	cs := newTestStore(t)
	if _, err := cs.Create("acme", "A", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Create("acme", "B", ""); err == nil {
		t.Fatal("duplicate id should fail")
	}
}

func TestClientUpdatePartialKeepsFlags(t *testing.T) {
	cs := newTestStore(t)
	c, _ := cs.Create("acme", "A", "")
	// A partial update that only changes the name must not touch flags.
	cur, err := cs.Update(c.ID, Client{Name: "B", Disabled: c.Disabled, Chargeable: c.Chargeable})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !cur.Chargeable || cur.Name != "B" {
		t.Errorf("flags or name lost: %+v", cur)
	}
}

func TestClientPersistenceRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	cs, _ := NewClientStore(root)
	if _, err := cs.Create("acme", "Acme", ""); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetSettings(Settings{DefaultPricePerGBHour: 0.09, DefaultRetentionHours: 48, MaxUploadBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	// Reload from disk.
	cs2, err := NewClientStore(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !cs2.Exists("acme") {
		t.Error("client not persisted")
	}
	if s := cs2.Settings(); s.DefaultPricePerGBHour != 0.09 || s.DefaultRetentionHours != 48 {
		t.Errorf("settings not persisted: %+v", s)
	}
}

func TestValidateClientIDRegex(t *testing.T) {
	good := []string{"acme", "a1", "my_client", "store-v2", "a.b", "1"}
	for _, id := range good {
		if err := ValidateClientID(id); err != nil {
			t.Errorf("id %q should be valid: %v", id, err)
		}
	}
}

func TestClientUpdateRetentionCap(t *testing.T) {
	cs := newTestStore(t)
	c, _ := cs.Create("acme", "A", "")
	if _, err := cs.Update(c.ID, Client{RetentionHours: DefaultMaxRetentionHours + 1, Chargeable: true, Disabled: false}); err == nil {
		t.Error("retention above cap should fail")
	}
}