package main

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// sampleAdminConfig mirrors the shape produced by deployment: sections the
// admin panel must not touch (acme, masquerade) around the managed ones.
const sampleAdminConfig = `listen: 0.0.0.0:443
acme:
  type: http
  ca: letsencrypt
  domains:
    - vpn.example.com
  email: acme@example.com
  dir: /var/lib/hysteria/acme
auth:
  type: userpass
  userpass:
    Admin: "e46b4a58d4d1fa4acbb9e26b9b0a1e6d9a0d5f8b32ab84bfb0fa1f2113afdc79"
masquerade:
  type: file
  file:
    dir: /var/www/masq
trafficStats:
  listen: 127.0.0.1:9765
  secret: "6c1ab3f2b41d2f8ee27b9cb9859f04fb32407a1b89a3d24ff7276b83b4078d04"
`

// confRoundTrip encodes the mutated tree, parses the result back and hands the
// re-parsed document over (the encoded text is returned for direct asserts).
func confRoundTrip(t *testing.T, root *yaml.Node) (*yaml.Node, string) {
	t.Helper()
	out, err := confEncode(root)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	other, err := confParse(text)
	if err != nil {
		t.Fatalf("round trip broke the file: %v\n---\n%s", err, text)
	}
	return other, text
}

func TestAdminConfigUsers(t *testing.T) {
	root, err := confParse(sampleAdminConfig)
	if err != nil {
		t.Fatal(err)
	}
	users, err := confUsers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0] != adminReserveUser {
		t.Fatalf("unexpected users: %v", users)
	}
	pass, err := confUser(root, adminReserveUser)
	if err != nil || len(pass) != 64 {
		t.Fatalf("owner password: %q %v", pass, err)
	}

	if err := confSetUser(root, "masha", "s3cret-PaSS"); err != nil {
		t.Fatal(err)
	}
	if err := confSetUser(root, adminReserveUser, "replacement"); err == nil {
		t.Fatal("the owner password must be protected")
	}

	other, text := confRoundTrip(t, root)
	if _, err := confUser(other, "masha"); err != nil {
		t.Fatalf("added user lost during round trip: %v", err)
	}
	var decoded struct {
		ACME struct {
			Domains []string `yaml:"domains"`
			Email   string   `yaml:"email"`
		} `yaml:"acme"`
		Masq struct {
			File struct {
				Dir string `yaml:"dir"`
			} `yaml:"file"`
		} `yaml:"masquerade"`
	}
	if err := yaml.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Join(decoded.ACME.Domains, ",") != "vpn.example.com" || decoded.Masq.File.Dir != "/var/www/masq" {
		t.Fatalf("untouched sections damaged: %#v", decoded)
	}
}

func TestAdminConfigRemoveUser(t *testing.T) {
	root, err := confParse(sampleAdminConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := confSetUser(root, "vova", "p2"); err != nil {
		t.Fatal(err)
	}
	if err := confRemoveUser(root, adminReserveUser); err == nil {
		t.Fatal("owner deletion must be rejected")
	}
	if err := confRemoveUser(root, "vova"); err != nil {
		t.Fatal(err)
	}
	if err := confRemoveUser(root, "vova"); err == nil {
		t.Fatal("deleting a missing user must fail")
	}
	other, _ := confRoundTrip(t, root)
	if _, err := confUser(other, "vova"); err == nil {
		t.Fatal("the user is still there")
	}
	if _, err := confUser(other, adminReserveUser); err != nil {
		t.Fatal(err)
	}
}

func TestAdminConfigTrafficStats(t *testing.T) {
	root, err := confParse(sampleAdminConfig)
	if err != nil {
		t.Fatal(err)
	}
	listen, secret, ok := confTrafficStats(root)
	if !ok || listen != "127.0.0.1:9765" || len(secret) != 64 {
		t.Fatalf("stats: %q %q %v", listen, secret, ok)
	}

	if err := confSetTrafficStats(root, "127.0.0.1:1234", "aabbcc"); err != nil {
		t.Fatal(err)
	}
	other, _ := confRoundTrip(t, root)
	if listen, secret, _ := confTrafficStats(other); listen != "127.0.0.1:1234" || secret != "aabbcc" {
		t.Fatalf("replaced stats: %q %q", listen, secret)
	}

	// a server without the section at all: adding must keep the whole file valid
	root, err = confParse("listen: :443\nauth:\n  type: userpass\n  userpass:\n    Admin: p\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := confTrafficStats(root); ok {
		t.Fatal("stats must be reported as absent")
	}
	if err := confSetTrafficStats(root, "127.0.0.1:9765", "cdef01"); err != nil {
		t.Fatal(err)
	}
	other, _ = confRoundTrip(t, root)
	if listen, secret, _ := confTrafficStats(other); listen != "127.0.0.1:9765" || secret != "cdef01" {
		t.Fatalf("added stats: %q %q", listen, secret)
	}
	if _, err := confUser(other, adminReserveUser); err != nil {
		t.Fatal(err)
	}
}

func TestAdminConfigScalarDataStaysData(t *testing.T) {
	root, err := confParse(sampleAdminConfig)
	if err != nil {
		t.Fatal(err)
	}
	// Values that could be mistaken for YAML syntax must survive untouched.
	for _, weird := range []string{"1234567", "p@ss\"`' \\ $", "0x10", "[a,b]", "true"} {
		if err := confSetUser(root, "u", weird); err != nil {
			t.Fatal(err)
		}
		other, _ := confRoundTrip(t, root)
		if pass, _ := confUser(other, "u"); pass != weird {
			t.Fatalf("scalar %q changed to %q", weird, pass)
		}
	}
}

func TestAdminConfigRejectsNonUserpass(t *testing.T) {
	root, err := confParse("listen: :443\nauth:\n  type: password\n  password: p\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confUsers(root); err == nil {
		t.Fatal("password auth must not be manageable")
	}
}

func TestAdminStorePersistsCredentials(t *testing.T) {
	dir := t.TempDir()
	s := newAdminStore(dir, false)
	if s.has("srv") {
		t.Fatal("a fresh store is empty")
	}
	if err := s.set("srv", adminCredential{Host: "203.0.113.9", Port: 22, Username: "root", Password: "pw", StatsPort: adminStatsListenPort, StatsSecret: "ss"}); err != nil {
		t.Fatal(err)
	}
	again := newAdminStore(dir, false)
	c, ok := again.get("srv")
	if !ok || c.Host != "203.0.113.9" || c.StatsSecret != "ss" {
		t.Fatalf("credentials lost: %#v %v", c, ok)
	}
	if err := again.remove("srv"); err != nil {
		t.Fatal(err)
	}
	if again.has("srv") {
		t.Fatal("credential removal failed")
	}
}

func TestAdminStoreFailedSaveKeepsPreviousState(t *testing.T) {
	s := newAdminStore(t.TempDir(), false)
	original := adminCredential{Host: "203.0.113.9", Password: "original"}
	if err := s.set("srv", original); err != nil {
		t.Fatal(err)
	}
	// A directory cannot be replaced by the store's temporary file.
	s.path = t.TempDir()
	if err := s.set("srv", adminCredential{Password: "replacement"}); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	if c, ok := s.get("srv"); !ok || c.Password != original.Password {
		t.Fatal("failed save changed the in-memory credential")
	}
	if err := s.set("new", original); err == nil || s.has("new") {
		t.Fatal("failed save retained a new credential")
	}
	if err := s.remove("srv"); err == nil || !s.has("srv") {
		t.Fatal("failed removal forgot the credential only in memory")
	}
}
