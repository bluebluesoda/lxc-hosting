package cfg

import (
	"strings"
	"testing"
)

func TestFieldForUnknown(t *testing.T) {
	if FieldFor("nope") != nil {
		t.Fatal("unknown key should not resolve")
	}
	if FieldFor("net.subnet") == nil {
		t.Fatal("net.subnet should resolve")
	}
}

func TestFieldKindsCovered(t *testing.T) {
	// The registry must classify every key it knows; nothing may fall through.
	for _, f := range Fields {
		if f.Key == "" || f.Get == nil || f.Assign == nil {
			t.Fatalf("field %q incomplete (key/get/assign)", f.Key)
		}
		if f.Kind.String() == "?" || f.Apply.String() == "?" {
			t.Fatalf("field %q has unknown kind/apply", f.Key)
		}
	}
}

func TestFieldValueReadsConfig(t *testing.T) {
	c := Default()
	c.Net.Subnet = "10.42.0.0/24"
	c.Panel.SessionDays = 7
	if v := FieldValue(c, "net.subnet"); v != "10.42.0.0/24" {
		t.Errorf("net.subnet = %q", v)
	}
	if v := FieldValue(c, "panel.session_days"); v != "7" {
		t.Errorf("panel.session_days = %q", v)
	}
	if v := FieldValue(c, "net.v4_forward"); v != "true" {
		t.Errorf("net.v4_forward default = %q", v)
	}
}

func TestAssignValidators(t *testing.T) {
	c := Default()

	if err := FieldFor("panel.session_days").Assign(c, "5"); err != nil {
		t.Fatalf("session_days=5: %v", err)
	}
	if c.Panel.SessionDays != 5 {
		t.Errorf("session_days = %d", c.Panel.SessionDays)
	}
	if err := FieldFor("panel.session_days").Assign(c, "0"); err == nil {
		t.Error("session_days=0 accepted")
	}
	if err := FieldFor("panel.session_days").Assign(c, "x"); err == nil {
		t.Error("session_days=x accepted")
	}

	for _, v := range []string{"true", "1", "on", "false", "0", "off"} {
		if err := FieldFor("net.v4_forward").Assign(c, v); err != nil {
			t.Errorf("v4_forward=%q: %v", v, err)
		}
	}
	if err := FieldFor("net.v4_forward").Assign(c, "maybe"); err == nil {
		t.Error("v4_forward=maybe accepted")
	}

	if err := FieldFor("net.ipv6_subnet").Assign(c, "2001:db8::/64"); err != nil {
		t.Fatalf("ipv6_subnet valid: %v", err)
	}
	if err := FieldFor("net.ipv6_subnet").Assign(c, "2001:db8::"); err == nil {
		t.Error("bare ipv6 address without prefix accepted")
	}
	if err := FieldFor("net.ipv6_subnet").Assign(c, ""); err != nil {
		t.Fatalf("ipv6_subnet empty (disable): %v", err)
	}
	if c.Net.IPv6Subnet != "" {
		t.Errorf("ipv6_subnet not cleared: %q", c.Net.IPv6Subnet)
	}

	if err := FieldFor("panel.listen").Assign(c, ":8443"); err != nil {
		t.Fatalf("listen valid: %v", err)
	}
	if err := FieldFor("panel.listen").Assign(c, "8443"); err == nil {
		t.Error("listen without port separator accepted")
	}
}

func TestImmutableAssignsRefuse(t *testing.T) {
	c := Default()
	for _, key := range []string{"net.subnet", "net.gateway", "lxd.pool", "lxd.bridge"} {
		if err := FieldFor(key).Assign(c, "whatever"); err == nil {
			t.Errorf("%s: immutable assign accepted", key)
		} else if !strings.Contains(err.Error(), "fixed at install") {
			t.Errorf("%s: unexpected error: %v", key, err)
		}
	}
}

func TestAdminPassHashManagedElsewhere(t *testing.T) {
	c := Default()
	if err := FieldFor("panel.admin_pass_hash").Assign(c, "x"); err == nil {
		t.Error("admin_pass_hash set via config accepted")
	}
	if v := FieldValue(c, "panel.admin_pass_hash"); !strings.Contains(v, "vps admin-passwd") {
		t.Errorf("admin_pass_hash list value = %q", v)
	}
}
