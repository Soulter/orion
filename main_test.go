package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestDeregisterServiceRemovesProxyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := appConfig{
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 38398,
		AuthToken:  "token",
	}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	state := serviceState{
		Services: map[string]*serviceRecord{
			"demo": {
				Name:       "demo",
				Slug:       "demo",
				Port:       38399,
				Domain:     "demo.edge.soulter.top",
				ConfigPath: filepath.Join(home, ".orion", "frpc.toml"),
				Status:     "running",
			},
		},
	}
	if err := saveState(state); err != nil {
		t.Fatalf("saveState returned error: %v", err)
	}
	if err := writeFRPCConfigFromState(cfg, state); err != nil {
		t.Fatalf("writeFRPCConfigFromState returned error: %v", err)
	}

	if err := deregisterService("demo"); err != nil {
		t.Fatalf("deregisterService returned error: %v", err)
	}

	updatedState, err := loadState()
	if err != nil {
		t.Fatalf("loadState returned error: %v", err)
	}
	if len(updatedState.Services) != 0 {
		t.Fatalf("expected no services after deregister, got %+v", updatedState.Services)
	}

	frpcPath, err := frpcConfigPath()
	if err != nil {
		t.Fatalf("frpcConfigPath returned error: %v", err)
	}
	content, err := os.ReadFile(frpcPath)
	if err != nil {
		t.Fatalf("os.ReadFile returned error: %v", err)
	}
	if strings.Contains(string(content), "name = \"demo\"") {
		t.Fatalf("proxy config still contains deregistered service: %s", string(content))
	}
	if !strings.Contains(string(content), "serverAddr = \"frps.edge.soulter.top\"") {
		t.Fatalf("frpc header unexpectedly missing: %s", string(content))
	}
}

func TestRunDownDeregistersService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	state := serviceState{
		Services: map[string]*serviceRecord{
			"demo": {
				Name:      "demo",
				Slug:      "demo",
				Port:      38399,
				Domain:    "demo.edge.soulter.top",
				Status:    "registered",
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := saveState(state); err != nil {
		t.Fatalf("saveState returned error: %v", err)
	}

	output, err := captureStdout(func() error {
		return runDown([]string{"-n", "demo"})
	})
	if err != nil {
		t.Fatalf("runDown returned error: %v", err)
	}
	if !strings.Contains(output, "deregistered demo") {
		t.Fatalf("unexpected output: %s", output)
	}

	updatedState, err := loadState()
	if err != nil {
		t.Fatalf("loadState returned error: %v", err)
	}
	if len(updatedState.Services) != 0 {
		t.Fatalf("expected no services after down, got %+v", updatedState.Services)
	}
}

func TestRunDownRequiresServiceName(t *testing.T) {
	t.Parallel()

	err := runDown(nil)
	if err == nil {
		t.Fatal("expected runDown to fail without -n")
	}
	if !strings.Contains(err.Error(), "missing required -n service name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunPairJoinRequiresConnectivitySuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldDialServer := dialServer
	dialServer = func(address string, timeout time.Duration) error {
		if address != "frps.edge.soulter.top:38398" {
			t.Fatalf("unexpected dial address: %s", address)
		}
		return nil
	}
	t.Cleanup(func() {
		dialServer = oldDialServer
	})

	raw, err := encodePairingToken(appConfig{
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 38398,
		AuthToken:  "token",
	})
	if err != nil {
		t.Fatalf("encodePairingToken returned error: %v", err)
	}

	if err := runPairJoin(raw); err != nil {
		t.Fatalf("runPairJoin returned error: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.ServerAddr != "frps.edge.soulter.top" || cfg.ServerPort != 38398 || cfg.BaseDomain != "edge.soulter.top" {
		t.Fatalf("unexpected config after pair join: %+v", cfg)
	}
}

func TestRunPairJoinFailsBeforeWritingConfigWhenConnectivityFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldDialServer := dialServer
	dialServer = func(address string, timeout time.Duration) error {
		return errors.New("connection refused")
	}
	t.Cleanup(func() {
		dialServer = oldDialServer
	})

	raw, err := encodePairingToken(appConfig{
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 38398,
		AuthToken:  "token",
	})
	if err != nil {
		t.Fatalf("encodePairingToken returned error: %v", err)
	}

	err = runPairJoin(raw)
	if err == nil {
		t.Fatal("expected runPairJoin to fail")
	}
	if !strings.Contains(err.Error(), "pair preflight failed") {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.ServerAddr != "" || cfg.ServerPort != 0 || cfg.BaseDomain != "" || cfg.AuthToken != "" {
		t.Fatalf("config should not be written on failed preflight: %+v", cfg)
	}
}

func TestRunListIncludesConnectivityStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := appConfig{
		BaseDomain: "edge.soulter.top",
		ServerAddr: "frps.edge.soulter.top",
		ServerPort: 38398,
		AuthToken:  "token",
	}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	state := serviceState{
		Services: map[string]*serviceRecord{
			"demo": {
				Name:       "demo",
				Slug:       "demo",
				Port:       38399,
				Domain:     "demo.edge.soulter.top",
				ConfigPath: filepath.Join(home, ".orion", "frpc.toml"),
				Status:     "registered",
			},
		},
	}
	if err := saveState(state); err != nil {
		t.Fatalf("saveState returned error: %v", err)
	}

	oldDialServer := dialServer
	dialServer = func(address string, timeout time.Duration) error {
		if address != "frps.edge.soulter.top:38398" {
			t.Fatalf("unexpected dial address: %s", address)
		}
		return nil
	}
	t.Cleanup(func() {
		dialServer = oldDialServer
	})

	oldProbe := probePublicEndpoint
	probePublicEndpoint = func(domain string, timeout time.Duration) error {
		if domain != "demo.edge.soulter.top" {
			t.Fatalf("unexpected domain probe: %s", domain)
		}
		return nil
	}
	t.Cleanup(func() {
		probePublicEndpoint = oldProbe
	})

	output, err := captureStdout(runList)
	if err != nil {
		t.Fatalf("captureStdout returned error: %v", err)
	}
	if !strings.Contains(output, "FRPS") || !strings.Contains(output, "PUBLIC") {
		t.Fatalf("missing connectivity headers: %s", output)
	}
	if !strings.Contains(output, "demo") || !strings.Contains(output, "ok") {
		t.Fatalf("missing service connectivity output: %s", output)
	}
}

func captureStdout(fn func() error) (string, error) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = writer

	runErr := fn()

	_ = writer.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		return "", err
	}
	_ = reader.Close()

	return buf.String(), runErr
}
