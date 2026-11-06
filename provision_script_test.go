package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestProvisionScriptUntrustedConfiguration(t *testing.T) {
	// Delimiters and command substitutions must remain data both in the shell
	// assignment and in the YAML (including an attempted heredoc termination).
	domain := "host'$(touch /tmp/hy2tun-must-not-run);.example.com"
	password := "secret\nHY2TUN_CONFIG\n$(id);'\"\\"
	email := "a\"\n: value@example.com"
	script, err := provisionScript(provisionRequest{Domain: domain, Email: email, SSHPort: 2222}, password)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "@@") {
		t.Fatal("unresolved template placeholder")
	}
	t.Run("bash syntax", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("remote Linux script syntax is checked on Unix; Windows bash may launch WSL")
		}
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("invalid shell syntax: %v: %s", err, out)
		}
	})
	start := strings.Index(script, "<<'HY2TUN_CONFIG'\n") + len("<<'HY2TUN_CONFIG'\n")
	end := strings.Index(script[start:], "\nHY2TUN_CONFIG") + start
	config, err := base64.StdEncoding.DecodeString(script[start:end])
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ACME struct {
			Domains []string `yaml:"domains"`
			Email   string   `yaml:"email"`
		} `yaml:"acme"`
		Auth struct {
			Users map[string]string `yaml:"userpass"`
		} `yaml:"auth"`
	}
	if err := yaml.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ACME.Domains) != 1 || decoded.ACME.Domains[0] != domain || decoded.ACME.Email != email || decoded.Auth.Users["Admin"] != password {
		t.Fatalf("configuration data changed: %#v", decoded)
	}
}

func TestProvisionScriptRefusesUnprivilegedBeforeMutations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix executable permissions and PATH semantics for the stub id command")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\nprintf '12345\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script, err := provisionScript(provisionRequest{Domain: "vpn.example.com", SSHPort: 22}, "password")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-se")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err == nil || string(out) != "HY2TUN_STAGE:preflight\nHY2TUN_ERROR:root_required\n" {
		t.Fatalf("expected a controlled preflight failure, got %v: %s", err, out)
	}
}

func TestProvisionScriptGuardsExistingServersAndReadiness(t *testing.T) {
	script, err := provisionScript(provisionRequest{Domain: "vpn.example.com", SSHPort: 2222}, "password")
	if err != nil {
		t.Fatal(err)
	}
	install := strings.Index(script, "apt-get ")
	for _, guard := range []string{"fail existing_installation", "fail ports_busy", "fail dns_mismatch"} {
		if i := strings.Index(script, guard); i < 0 || i > install {
			t.Fatalf("%s does not precede installation", guard)
		}
	}
	ready := strings.Index(script, "printf 'HY2TUN_READY")
	for _, check := range []string{"[ \"$ready\" = true ] || fail certificate_failed", "systemctl is-enabled --quiet hysteria-server.service"} {
		if i := strings.Index(script, check); i < 0 || i > ready {
			t.Fatalf("%s does not precede success", check)
		}
	}
	if strings.Contains(script, "upgrade") || strings.Contains(script, "ufw reset") {
		t.Fatal("deployment must not upgrade OS or reset existing firewall rules")
	}
}

func TestProvisionScriptRejectsIncompleteInput(t *testing.T) {
	for _, req := range []provisionRequest{{}, {Domain: "vpn.example.com", SSHPort: -1}, {Domain: "vpn.example.com", SSHPort: 65536}} {
		if _, err := provisionScript(req, "password"); err == nil {
			t.Fatal("incomplete input accepted")
		}
	}
	if _, err := provisionScript(provisionRequest{Domain: "vpn.example.com", SSHPort: 22}, ""); err == nil {
		t.Fatal("empty VPN password accepted")
	}
}
