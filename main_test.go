package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugifyName(t *testing.T) {
	t.Parallel()

	slug, err := slugifyName("My_Service.v1")
	if err != nil {
		t.Fatalf("slugifyName returned error: %v", err)
	}
	if slug != "my-service-v1" {
		t.Fatalf("unexpected slug: %s", slug)
	}
}

func TestUpsertProxyBlockInsert(t *testing.T) {
	t.Parallel()

	cfg := appConfig{
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 7000,
		AuthToken:  "token",
	}
	got := renderFRPCConfig(cfg, []proxySpec{{
		Name:       "llamacpp",
		LocalIP:    "127.0.0.1",
		LocalPort:  38399,
		CustomHost: "llamacpp.edge.soulter.top",
	}})
	if !strings.Contains(got, "serverAddr = \"frps.edge.soulter.top\"") {
		t.Fatalf("frpc header missing: %s", got)
	}
	if !strings.Contains(got, "name = \"llamacpp\"") {
		t.Fatalf("proxy block missing: %s", got)
	}
}

func TestRenderFRPSConfig(t *testing.T) {
	t.Parallel()

	cfg := appConfig{
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 7000,
		AuthToken:  "token",
	}
	got := renderFRPSConfig(cfg, serverStartOptions{
		publicHost: "frps.edge.soulter.top",
		bindAddr:   "0.0.0.0",
		bindPort:   7000,
		vhostPort:  8080,
	})
	if !strings.Contains(got, "bindPort = 7000") {
		t.Fatalf("frps bind port missing: %s", got)
	}
	if !strings.Contains(got, "auth.token = \"token\"") {
		t.Fatalf("frps auth token missing: %s", got)
	}
}

func TestRenderProxyBlock(t *testing.T) {
	t.Parallel()

	block := renderProxyBlock(proxySpec{
		Name:       "llamacpp",
		LocalIP:    "127.0.0.1",
		LocalPort:  38399,
		CustomHost: "llamacpp.edge.soulter.top",
	})

	if !strings.Contains(block, "type = \"http\"") {
		t.Fatalf("unexpected block: %s", block)
	}
	if !strings.Contains(block, "localPort = 38399") {
		t.Fatalf("unexpected block: %s", block)
	}
}

func TestParseServiceFlagsWithoutConfig(t *testing.T) {
	t.Parallel()

	opts, err := parseServiceFlags("up", []string{"-n", "llamacpp", "-p", "38399"})
	if err != nil {
		t.Fatalf("parseServiceFlags returned error: %v", err)
	}
	if opts.name != "llamacpp" || opts.port != 38399 {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestEncodeDecodePairingToken(t *testing.T) {
	t.Parallel()

	cfg := appConfig{
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 7000,
		AuthToken:  "token",
	}
	raw, err := encodePairingToken(cfg)
	if err != nil {
		t.Fatalf("encodePairingToken returned error: %v", err)
	}
	token, err := decodePairingToken(raw)
	if err != nil {
		t.Fatalf("decodePairingToken returned error: %v", err)
	}
	if token.BaseDomain != cfg.BaseDomain || token.ServerAddr != cfg.ServerAddr || token.ServerPort != cfg.ServerPort || token.AuthToken != cfg.AuthToken {
		t.Fatalf("unexpected token: %+v", token)
	}
}

func TestDecodePairingTokenVersionMismatch(t *testing.T) {
	t.Parallel()

	rawJSON, err := json.Marshal(pairingToken{
		Version:    99,
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 7000,
		AuthToken:  "token",
	})
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(rawJSON)
	if _, err := decodePairingToken(raw); err == nil {
		t.Fatal("expected version mismatch error")
	}
}

func TestFRPCConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := frpcConfigPath()
	if err != nil {
		t.Fatalf("frpcConfigPath returned error: %v", err)
	}

	want := filepath.Join(home, ".orion", "frpc.toml")
	if got != want {
		t.Fatalf("unexpected path: got %s want %s", got, want)
	}
}

func TestFRPSConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := frpsConfigPath()
	if err != nil {
		t.Fatalf("frpsConfigPath returned error: %v", err)
	}

	want := filepath.Join(home, ".orion", "frps.toml")
	if got != want {
		t.Fatalf("unexpected path: got %s want %s", got, want)
	}
}

func TestBundledFRPCPath(t *testing.T) {
	t.Parallel()

	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable returned error: %v", err)
	}
	exeDir := filepath.Dir(exePath)
	expected := filepath.Join(exeDir, executableName("frpc"))

	got, _, err := bundledFRPCPath()
	if err != nil {
		t.Fatalf("bundledFRPCPath returned error: %v", err)
	}
	if got != expected {
		t.Fatalf("unexpected frpc path: got %s want %s", got, expected)
	}
}

func TestValidateServiceConflicts(t *testing.T) {
	t.Parallel()

	state := serviceState{
		Services: map[string]*serviceRecord{
			"a": {Name: "a", Slug: "same", Port: 3000},
		},
	}
	if err := validateServiceConflicts(state, "b", "same", 4000); err == nil {
		t.Fatal("expected slug conflict")
	}
	if err := validateServiceConflicts(state, "b", "other", 3000); err == nil {
		t.Fatal("expected port conflict")
	}
}
