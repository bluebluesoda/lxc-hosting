package mgr

import (
	"path/filepath"
	"testing"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
)

// TestAddDomainRejectedWhenV4Off: with v4_forward=false the domain proxy is not
// offered, so adding a domain must be rejected before any traefik write.
func TestAddDomainRejectedWhenV4Off(t *testing.T) {
	c := cfg.Default()
	c.Net.V4Forward = false
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.CreateUser("alice", "x", "10.115.0.2", 1, 30001, 10000, 1, 1024, 10); err != nil {
		t.Fatal(err)
	}
	m := New(c, d)
	if err := m.AddDomain("alice", "example.com"); err == nil {
		t.Fatal("AddDomain should be rejected when v4_forward is false")
	}
}
