package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	defaultLocalIP       = "127.0.0.1"
	defaultBindAddr      = "0.0.0.0"
	defaultServerPort    = 38398
	defaultVHostHTTPPort = 38397
	pairJoinTimeout      = 3 * time.Second
	statusProbeTimeout   = 3 * time.Second
	configFileName       = "config.json"
	stateFileName        = "services.json"
	frpcFileName         = "frpc.toml"
	frpsFileName         = "frps.toml"
	logDirName           = "logs"
	pairingTokenVersion  = 1
)

type appConfig struct {
	BaseDomain string `json:"base_domain"`
	ServerAddr string `json:"server_addr,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	AuthToken  string `json:"auth_token,omitempty"`
}

type serviceState struct {
	Services map[string]*serviceRecord `json:"services"`
	FRPC     *processRecord            `json:"frpc,omitempty"`
	FRPS     *processRecord            `json:"frps,omitempty"`
}

type serviceRecord struct {
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Port         int      `json:"port"`
	Domain       string   `json:"domain"`
	HTTPUser     string   `json:"http_user,omitempty"`
	HTTPPassword string   `json:"http_password,omitempty"`
	ConfigPath   string   `json:"config_path"`
	Status       string   `json:"status"`
	PID          int      `json:"pid,omitempty"`
	Command      []string `json:"command,omitempty"`
	LastExitCode *int     `json:"last_exit_code,omitempty"`
	UpdatedAt    string   `json:"updated_at"`
}

type processRecord struct {
	Name       string `json:"name"`
	PID        int    `json:"pid,omitempty"`
	Status     string `json:"status"`
	BinaryPath string `json:"binary_path"`
	ConfigPath string `json:"config_path"`
	LogPath    string `json:"log_path"`
	UpdatedAt  string `json:"updated_at"`
}

type proxySpec struct {
	Name         string
	LocalIP      string
	LocalPort    int
	CustomHost   string
	HTTPUser     string
	HTTPPassword string
}

type serviceOptions struct {
	name         string
	port         int
	httpUser     string
	httpPassword string
	command      []string
}

type serverStartOptions struct {
	publicHost string
	bindAddr   string
	bindPort   int
	vhostPort  int
}

type pairingToken struct {
	Version    int    `json:"version"`
	BaseDomain string `json:"base_domain"`
	ServerAddr string `json:"server_addr"`
	ServerPort int    `json:"server_port"`
	AuthToken  string `json:"auth_token"`
}

var dialServer = func(address string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

var probePublicEndpoint = func(domain string, timeout time.Duration) error {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequest(http.MethodHead, "https://"+domain, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		return nil
	}

	req, err = http.NewRequest(http.MethodGet, "https://"+domain, nil)
	if err != nil {
		return err
	}
	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()
	return nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 1
	}

	switch args[0] {
	case "up":
		if err := runUp(args[1:]); err != nil {
			return printError(err)
		}
		return 0
	case "down":
		if err := runDown(args[1:]); err != nil {
			return printError(err)
		}
		return 0
	case "serve":
		code, err := runServe(args[1:])
		if err != nil {
			return printError(err)
		}
		return code
	case "list":
		if err := runList(); err != nil {
			return printError(err)
		}
		return 0
	case "config":
		if err := runConfig(args[1:]); err != nil {
			return printError(err)
		}
		return 0
	case "server":
		if err := runServer(args[1:]); err != nil {
			return printError(err)
		}
		return 0
	case "pair":
		if err := runPair(args[1:]); err != nil {
			return printError(err)
		}
		return 0
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		return printError(fmt.Errorf("unknown command %q", args[0]))
	}
}

func runUp(args []string) error {
	opts, err := parseServiceFlags("up", args)
	if err != nil {
		return err
	}

	record, err := registerService(opts)
	if err != nil {
		return err
	}

	fmt.Printf("registered %s -> https://%s\n", record.Name, record.Domain)
	return nil
}

func runDown(args []string) error {
	name, err := parseServiceNameFlags("down", args)
	if err != nil {
		return err
	}
	if err := deregisterService(name); err != nil {
		return err
	}
	fmt.Printf("deregistered %s\n", name)
	return nil
}

func runServe(args []string) (int, error) {
	opts, err := parseServiceFlags("serve", args)
	if err != nil {
		return 1, err
	}
	if len(opts.command) == 0 {
		return 1, errors.New("serve requires a command after --")
	}

	record, err := registerService(opts)
	if err != nil {
		return 1, err
	}

	cmd := exec.Command(opts.command[0], opts.command[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start service command: %w", err)
	}

	record.Status = "running"
	record.PID = cmd.Process.Pid
	record.Command = append([]string(nil), opts.command...)
	record.LastExitCode = nil
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := updateService(record); err != nil {
		_ = cmd.Process.Kill()
		return 1, err
	}

	fmt.Printf("serving %s -> https://%s (pid=%d)\n", record.Name, record.Domain, record.PID)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)

	go func() {
		for sig := range signals {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()

	waitErr := cmd.Wait()
	exitCode := exitCodeFromError(waitErr)
	if err := deregisterService(record.Name); err != nil {
		return exitCode, err
	}

	if waitErr != nil {
		return exitCode, nil
	}
	return 0, nil
}

func runList() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	state, err := loadState()
	if err != nil {
		return err
	}
	changed := false
	if refreshProcessStatus(state.FRPC) {
		changed = true
	}
	if refreshProcessStatus(state.FRPS) {
		changed = true
	}
	records := make([]*serviceRecord, 0, len(state.Services))
	for _, record := range state.Services {
		display := displayServiceStatus(record)
		if display != record.Status && record.Status == "running" && record.PID > 0 && !processExists(record.PID) {
			record.Status = "stopped"
			record.PID = 0
			record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			changed = true
		}
		records = append(records, record)
	}
	if changed {
		if err := saveState(state); err != nil {
			return err
		}
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})

	frpsStatus := "not-paired"
	if strings.TrimSpace(cfg.ServerAddr) != "" && cfg.ServerPort > 0 {
		frpsStatus = probeFRPSConnectivity(cfg.ServerAddr, cfg.ServerPort)
	}

	fmt.Println("PROCESS\tSTATUS\tPID\tCONFIG\tLOG")
	fmt.Printf(
		"frpc\t%s\t%s\t%s\t%s\n",
		displayProcessStatus(state.FRPC),
		displayPID(state.FRPC),
		displayProcessField(state.FRPC, "ConfigPath"),
		displayProcessField(state.FRPC, "LogPath"),
	)
	fmt.Printf(
		"frps\t%s\t%s\t%s\t%s\n",
		displayProcessStatus(state.FRPS),
		displayPID(state.FRPS),
		displayProcessField(state.FRPS, "ConfigPath"),
		displayProcessField(state.FRPS, "LogPath"),
	)

	if len(records) == 0 {
		fmt.Println()
		fmt.Println("no services registered")
		return nil
	}

	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tFRPS\tPUBLIC\tPORT\tDOMAIN\tCONFIG")
	for _, record := range records {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			record.Name,
			displayServiceStatus(record),
			frpsStatus,
			probePublicStatus(record.Domain),
			record.Port,
			record.Domain,
			record.ConfigPath,
		)
	}
	return tw.Flush()
}

func runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("config requires a subcommand")
	}

	switch args[0] {
	case "set":
		if len(args) != 3 {
			return errors.New("usage: orion config set base_domain <domain>")
		}
		if args[1] != "base_domain" {
			return fmt.Errorf("unknown config key %q", args[1])
		}
		domain := strings.ToLower(strings.TrimSpace(args[2]))
		if err := validateBaseDomain(domain); err != nil {
			return err
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		cfg.BaseDomain = domain
		if err := saveConfig(cfg); err != nil {
			return err
		}

		state, err := loadState()
		if err != nil {
			return err
		}
		if len(state.Services) > 0 && isClientConfigured(cfg) {
			if err := syncClientConfigAndProcess(cfg, state); err != nil {
				return err
			}
		}

		fmt.Printf("base_domain = %s\n", cfg.BaseDomain)
		return nil
	case "show":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		frpcPath, err := frpcConfigPath()
		if err != nil {
			return err
		}
		frpsPath, err := frpsConfigPath()
		if err != nil {
			return err
		}
		fmt.Printf("base_domain = %s\n", orDefault(cfg.BaseDomain, "(not set)"))
		fmt.Printf("server_addr = %s\n", orDefault(cfg.ServerAddr, "(not paired)"))
		fmt.Printf("server_port = %s\n", displayInt(cfg.ServerPort))
		fmt.Printf("frpc_config = %s\n", frpcPath)
		fmt.Printf("frps_config = %s\n", frpsPath)

		frpcPathBin, frpcFound, err := bundledFRPCPath()
		if err != nil {
			return err
		}
		if frpcFound {
			fmt.Printf("frpc_binary = %s\n", frpcPathBin)
		} else {
			fmt.Printf("frpc_binary = (not found, expected near orion binary)\n")
		}

		frpsPathBin, frpsFound, err := bundledFRPSPath()
		if err != nil {
			return err
		}
		if frpsFound {
			fmt.Printf("frps_binary = %s\n", frpsPathBin)
		} else {
			fmt.Printf("frps_binary = (not found, expected near orion binary)\n")
		}
		return nil
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func runServer(args []string) error {
	if len(args) == 0 {
		return errors.New("server requires a subcommand")
	}

	switch args[0] {
	case "start":
		return runServerStart(args[1:])
	case "status":
		return runServerStatus()
	case "stop":
		return runServerStop()
	default:
		return fmt.Errorf("unknown server subcommand %q", args[0])
	}
}

func runServerStart(args []string) error {
	opts, err := parseServerStartFlags(args)
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.BaseDomain == "" {
		return errors.New("base_domain is not set; run: orion config set base_domain example.com")
	}
	if err := validateBaseDomain(cfg.BaseDomain); err != nil {
		return err
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken, err = randomToken(24)
		if err != nil {
			return err
		}
	}
	cfg.ServerAddr = strings.TrimSpace(opts.publicHost)
	cfg.ServerPort = opts.bindPort
	if err := saveConfig(cfg); err != nil {
		return err
	}

	frpsCfgPath, err := frpsConfigPath()
	if err != nil {
		return err
	}
	if err := writeFRPSConfig(frpsCfgPath, cfg, opts); err != nil {
		return err
	}

	state, err := loadState()
	if err != nil {
		return err
	}
	if err := restartFRPS(state, frpsCfgPath); err != nil {
		return err
	}

	token, err := encodePairingToken(cfg)
	if err != nil {
		return err
	}

	fmt.Printf("frps started -> %s:%d\n", cfg.ServerAddr, cfg.ServerPort)
	fmt.Printf("pair token: %s\n", token)
	fmt.Printf("client join: orion pair join %s\n", token)
	return nil
}

func runServerStatus() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	state, err := loadState()
	if err != nil {
		return err
	}
	if refreshProcessStatus(state.FRPS) {
		if err := saveState(state); err != nil {
			return err
		}
	}

	token, tokenErr := encodePairingToken(cfg)
	fmt.Printf("server_addr = %s\n", orDefault(cfg.ServerAddr, "(not set)"))
	fmt.Printf("server_port = %s\n", displayInt(cfg.ServerPort))
	fmt.Printf("base_domain = %s\n", orDefault(cfg.BaseDomain, "(not set)"))
	fmt.Printf("frps_status = %s\n", displayProcessStatus(state.FRPS))
	if state.FRPS != nil {
		fmt.Printf("frps_pid = %s\n", displayPID(state.FRPS))
		fmt.Printf("frps_config = %s\n", state.FRPS.ConfigPath)
		fmt.Printf("frps_log = %s\n", state.FRPS.LogPath)
	}
	if tokenErr == nil {
		fmt.Printf("pair token: %s\n", token)
	}
	return nil
}

func runServerStop() error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if err := stopManagedProcess(state.FRPS); err != nil {
		return err
	}
	if err := saveState(state); err != nil {
		return err
	}
	fmt.Println("frps stopped")
	return nil
}

func runPair(args []string) error {
	if len(args) == 0 {
		return errors.New("pair requires a subcommand")
	}

	switch args[0] {
	case "show":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		token, err := encodePairingToken(cfg)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	case "join":
		if len(args) != 2 {
			return errors.New("usage: orion pair join <token>")
		}
		return runPairJoin(args[1])
	default:
		return fmt.Errorf("unknown pair subcommand %q", args[0])
	}
}

func runPairJoin(raw string) error {
	token, err := decodePairingToken(raw)
	if err != nil {
		return err
	}
	if err := validateBaseDomain(token.BaseDomain); err != nil {
		return err
	}
	if strings.TrimSpace(token.ServerAddr) == "" {
		return errors.New("pair token is missing server_addr")
	}
	if token.ServerPort <= 0 || token.ServerPort > 65535 {
		return errors.New("pair token has invalid server_port")
	}
	if strings.TrimSpace(token.AuthToken) == "" {
		return errors.New("pair token is missing auth_token")
	}
	if err := verifyPairConnectivity(token); err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.BaseDomain = token.BaseDomain
	cfg.ServerAddr = token.ServerAddr
	cfg.ServerPort = token.ServerPort
	cfg.AuthToken = token.AuthToken
	if err := saveConfig(cfg); err != nil {
		return err
	}

	state, err := loadState()
	if err != nil {
		return err
	}
	if err := writeFRPCConfigFromState(cfg, state); err != nil {
		return err
	}
	if len(state.Services) > 0 {
		if err := restartFRPC(cfg, state); err != nil {
			return err
		}
	}

	fmt.Printf("paired to %s:%d for *.%s\n", cfg.ServerAddr, cfg.ServerPort, cfg.BaseDomain)
	return nil
}

func verifyPairConnectivity(token pairingToken) error {
	address := net.JoinHostPort(token.ServerAddr, strconv.Itoa(token.ServerPort))
	if err := dialServer(address, pairJoinTimeout); err != nil {
		return fmt.Errorf("pair preflight failed: cannot connect to %s: %w", address, err)
	}
	return nil
}

func probeFRPSConnectivity(serverAddr string, serverPort int) string {
	address := net.JoinHostPort(serverAddr, strconv.Itoa(serverPort))
	if err := dialServer(address, statusProbeTimeout); err != nil {
		return "down"
	}
	return "ok"
}

func probePublicStatus(domain string) string {
	if strings.TrimSpace(domain) == "" {
		return "unknown"
	}
	if err := probePublicEndpoint(domain, statusProbeTimeout); err != nil {
		return "down"
	}
	return "ok"
}

func parseServiceFlags(command string, args []string) (serviceOptions, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var opts serviceOptions
	fs.StringVar(&opts.name, "n", "", "service name")
	fs.IntVar(&opts.port, "p", 0, "local port")
	fs.StringVar(&opts.httpUser, "http_user", "", "HTTP Basic Auth username")
	fs.StringVar(&opts.httpPassword, "http_password", "", "HTTP Basic Auth password")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.name == "" {
		return opts, errors.New("missing required -n service name")
	}
	if opts.port <= 0 || opts.port > 65535 {
		return opts, errors.New("missing or invalid -p local port")
	}
	if (opts.httpUser == "") != (opts.httpPassword == "") {
		return opts, errors.New("--http_user and --http_password must be provided together")
	}

	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	opts.command = rest
	return opts, nil
}

func parseServiceNameFlags(command string, args []string) (string, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var name string
	fs.StringVar(&name, "n", "", "service name")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("usage: orion %s -n <service>", command)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("missing required -n service name")
	}
	return name, nil
}

func parseServerStartFlags(args []string) (serverStartOptions, error) {
	fs := flag.NewFlagSet("server start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	opts := serverStartOptions{}
	fs.StringVar(&opts.publicHost, "public-host", "", "public host clients use to connect")
	fs.StringVar(&opts.bindAddr, "bind-addr", defaultBindAddr, "frps bind address")
	fs.IntVar(&opts.bindPort, "bind-port", defaultServerPort, "frps bind port")
	fs.IntVar(&opts.vhostPort, "vhost-http-port", defaultVHostHTTPPort, "frps HTTP vhost port")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if strings.TrimSpace(opts.publicHost) == "" {
		return opts, errors.New("missing required --public-host")
	}
	if opts.bindPort <= 0 || opts.bindPort > 65535 {
		return opts, errors.New("invalid --bind-port")
	}
	if opts.vhostPort <= 0 || opts.vhostPort > 65535 {
		return opts, errors.New("invalid --vhost-http-port")
	}
	return opts, nil
}

func registerService(opts serviceOptions) (*serviceRecord, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if !isClientConfigured(cfg) {
		return nil, errors.New("client is not paired; run: orion pair join <token>")
	}

	slug, err := slugifyName(opts.name)
	if err != nil {
		return nil, err
	}

	state, err := loadState()
	if err != nil {
		return nil, err
	}
	if err := validateServiceConflicts(state, opts.name, slug, opts.port); err != nil {
		return nil, err
	}

	frpcPath, err := frpcConfigPath()
	if err != nil {
		return nil, err
	}
	domain := slug + "." + cfg.BaseDomain

	record, ok := state.Services[opts.name]
	if !ok || record == nil {
		record = &serviceRecord{Name: opts.name}
		state.Services[opts.name] = record
	}

	running := record.Status == "running" && record.PID > 0 && processExists(record.PID)
	record.Name = opts.name
	record.Slug = slug
	record.Port = opts.port
	record.Domain = domain
	record.HTTPUser = opts.httpUser
	record.HTTPPassword = opts.httpPassword
	record.ConfigPath = frpcPath
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if !running {
		record.Status = "registered"
		record.PID = 0
		record.Command = nil
		record.LastExitCode = nil
	}

	if err := syncClientConfigAndProcess(cfg, state); err != nil {
		return nil, err
	}

	copy := *record
	return &copy, nil
}

func syncClientConfigAndProcess(cfg appConfig, state serviceState) error {
	if err := writeFRPCConfigFromState(cfg, state); err != nil {
		return err
	}
	if len(state.Services) == 0 {
		if err := stopManagedProcess(state.FRPC); err != nil {
			return err
		}
		return saveState(state)
	}
	return restartFRPC(cfg, state)
}

func deregisterService(name string) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if state.Services == nil {
		state.Services = make(map[string]*serviceRecord)
	}
	if _, ok := state.Services[name]; !ok {
		return nil
	}
	delete(state.Services, name)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if !isClientConfigured(cfg) {
		return saveState(state)
	}
	return syncClientConfigAndProcess(cfg, state)
}

func updateService(record *serviceRecord) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if state.Services == nil {
		state.Services = make(map[string]*serviceRecord)
	}
	copy := *record
	state.Services[record.Name] = &copy
	return saveState(state)
}

func validateServiceConflicts(state serviceState, name string, slug string, port int) error {
	for existingName, record := range state.Services {
		if record == nil || existingName == name {
			continue
		}
		if record.Slug == slug {
			return fmt.Errorf("service %q conflicts with %q because both resolve to the same hostname label", name, existingName)
		}
		if record.Port == port {
			return fmt.Errorf("service %q conflicts with %q because both use local port %d", name, existingName, port)
		}
	}
	return nil
}

func writeFRPCConfigFromState(cfg appConfig, state serviceState) error {
	path, err := frpcConfigPath()
	if err != nil {
		return err
	}

	proxies := make([]proxySpec, 0, len(state.Services))
	for _, record := range state.Services {
		if record == nil {
			continue
		}
		proxies = append(proxies, proxySpec{
			Name:         record.Name,
			LocalIP:      defaultLocalIP,
			LocalPort:    record.Port,
			CustomHost:   record.Domain,
			HTTPUser:     record.HTTPUser,
			HTTPPassword: record.HTTPPassword,
		})
	}
	sort.Slice(proxies, func(i, j int) bool {
		return proxies[i].Name < proxies[j].Name
	})

	content := renderFRPCConfig(cfg, proxies)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	return writeFileAtomic(path, []byte(content), 0o644)
}

func writeFRPSConfig(path string, cfg appConfig, opts serverStartOptions) error {
	content := renderFRPSConfig(cfg, opts)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	return writeFileAtomic(path, []byte(content), 0o644)
}

func restartFRPC(cfg appConfig, state serviceState) error {
	if !isClientConfigured(cfg) {
		return nil
	}
	path, err := frpcConfigPath()
	if err != nil {
		return err
	}
	binaryPath, found, err := bundledFRPCPath()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("frpc binary not found; expected %s", binaryPath)
	}
	record, err := restartManagedProcess("frpc", state.FRPC, binaryPath, path, []string{"-c", path})
	if err != nil {
		return err
	}
	state.FRPC = record
	return saveState(state)
}

func restartFRPS(state serviceState, configPath string) error {
	binaryPath, found, err := bundledFRPSPath()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("frps binary not found; expected %s", binaryPath)
	}
	record, err := restartManagedProcess("frps", state.FRPS, binaryPath, configPath, []string{"-c", configPath})
	if err != nil {
		return err
	}
	state.FRPS = record
	return saveState(state)
}

func restartManagedProcess(name string, current *processRecord, binaryPath string, configPath string, args []string) (*processRecord, error) {
	if err := stopManagedProcess(current); err != nil {
		return nil, err
	}

	logPath, err := logFilePath(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureBackgroundCommand(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}

	return &processRecord{
		Name:       name,
		PID:        cmd.Process.Pid,
		Status:     "running",
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		LogPath:    logPath,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func stopManagedProcess(record *processRecord) error {
	if record == nil {
		return nil
	}
	if record.PID > 0 && processExists(record.PID) {
		if err := terminateProcess(record.PID); err != nil {
			return fmt.Errorf("stop %s: %w", record.Name, err)
		}
	}
	record.PID = 0
	record.Status = "stopped"
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return nil
}

func refreshProcessStatus(record *processRecord) bool {
	if record == nil {
		return false
	}
	if record.Status == "running" && record.PID > 0 && !processExists(record.PID) {
		record.PID = 0
		record.Status = "stopped"
		record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return true
	}
	return false
}

func renderFRPCConfig(cfg appConfig, proxies []proxySpec) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "serverAddr = %q\n", cfg.ServerAddr)
	fmt.Fprintf(&builder, "serverPort = %d\n", cfg.ServerPort)
	builder.WriteString("loginFailExit = false\n")
	fmt.Fprintf(&builder, "auth.method = %q\n", "token")
	fmt.Fprintf(&builder, "auth.token = %q\n", cfg.AuthToken)

	for _, proxy := range proxies {
		builder.WriteString("\n")
		builder.WriteString(renderProxyBlock(proxy))
	}
	return builder.String()
}

func renderFRPSConfig(cfg appConfig, opts serverStartOptions) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "bindAddr = %q\n", opts.bindAddr)
	fmt.Fprintf(&builder, "bindPort = %d\n", opts.bindPort)
	fmt.Fprintf(&builder, "vhostHTTPPort = %d\n", opts.vhostPort)
	fmt.Fprintf(&builder, "auth.method = %q\n", "token")
	fmt.Fprintf(&builder, "auth.token = %q\n", cfg.AuthToken)
	return builder.String()
}

func renderProxyBlock(spec proxySpec) string {
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"[[proxies]]\nname = %q\ntype = %q\nlocalIP = %q\nlocalPort = %d\ncustomDomains = [%q]\n",
		spec.Name,
		"http",
		spec.LocalIP,
		spec.LocalPort,
		spec.CustomHost,
	)
	if spec.HTTPUser != "" && spec.HTTPPassword != "" {
		fmt.Fprintf(&builder, "httpUser = %q\n", spec.HTTPUser)
		fmt.Fprintf(&builder, "httpPassword = %q\n", spec.HTTPPassword)
	}
	return builder.String()
}

func encodePairingToken(cfg appConfig) (string, error) {
	if !isClientConfigured(cfg) {
		return "", errors.New("server is not fully configured; run: orion server start --public-host <host>")
	}
	token := pairingToken{
		Version:    pairingTokenVersion,
		BaseDomain: cfg.BaseDomain,
		ServerAddr: cfg.ServerAddr,
		ServerPort: cfg.ServerPort,
		AuthToken:  cfg.AuthToken,
	}
	data, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("marshal pair token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodePairingToken(raw string) (pairingToken, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return pairingToken{}, fmt.Errorf("decode pair token: %w", err)
	}
	var token pairingToken
	if err := json.Unmarshal(data, &token); err != nil {
		return pairingToken{}, fmt.Errorf("parse pair token: %w", err)
	}
	if token.Version != pairingTokenVersion {
		return pairingToken{}, fmt.Errorf("unsupported pair token version %d", token.Version)
	}
	return token, nil
}

func randomToken(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func loadConfig() (appConfig, error) {
	path, err := configPath()
	if err != nil {
		return appConfig{}, err
	}

	var cfg appConfig
	if err := readJSONFile(path, &cfg); err != nil {
		return appConfig{}, err
	}
	return cfg, nil
}

func saveConfig(cfg appConfig) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	return writeJSONFile(path, cfg)
}

func loadState() (serviceState, error) {
	path, err := statePath()
	if err != nil {
		return serviceState{}, err
	}

	state := serviceState{
		Services: make(map[string]*serviceRecord),
	}
	if err := readJSONFile(path, &state); err != nil {
		return serviceState{}, err
	}
	if state.Services == nil {
		state.Services = make(map[string]*serviceRecord)
	}
	return state, nil
}

func saveState(state serviceState) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	return writeJSONFile(path, state)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	return writeFileAtomic(path, data, 0o644)
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tempPath := temp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	success = true
	return nil
}

func configPath() (string, error) {
	root, err := storageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, configFileName), nil
}

func statePath() (string, error) {
	root, err := storageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, stateFileName), nil
}

func frpcConfigPath() (string, error) {
	root, err := storageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, frpcFileName), nil
}

func frpsConfigPath() (string, error) {
	root, err := storageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, frpsFileName), nil
}

func logFilePath(name string) (string, error) {
	root, err := storageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, logDirName, name+".log"), nil
}

func storageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".orion"), nil
}

func bundledFRPCPath() (string, bool, error) {
	return bundledBinaryPath("frpc")
}

func bundledFRPSPath() (string, bool, error) {
	return bundledBinaryPath("frps")
}

func bundledBinaryPath(name string) (string, bool, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", false, fmt.Errorf("resolve executable path: %w", err)
	}
	exeDir := filepath.Dir(exePath)
	fileName := executableName(name)
	candidates := []string{
		filepath.Join(exeDir, fileName),
		filepath.Join(exeDir, "bin", fileName),
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("stat %s: %w", name, err)
		}
	}
	return candidates[0], false, nil
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func slugifyName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", errors.New("service name cannot be empty")
	}

	var builder strings.Builder
	lastHyphen := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastHyphen = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || r == '.' || r == ' ':
			if builder.Len() > 0 && !lastHyphen {
				builder.WriteByte('-')
				lastHyphen = true
			}
		default:
			if builder.Len() > 0 && !lastHyphen {
				builder.WriteByte('-')
				lastHyphen = true
			}
		}
	}

	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "", fmt.Errorf("service name %q does not produce a valid hostname label", name)
	}
	return slug, nil
}

func validateBaseDomain(domain string) error {
	if domain == "" {
		return errors.New("base_domain cannot be empty")
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return errors.New("base_domain must contain at least one dot")
	}
	for _, label := range labels {
		if label == "" {
			return fmt.Errorf("base_domain %q is invalid", domain)
		}
		for i, r := range label {
			valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
			if !valid {
				return fmt.Errorf("base_domain %q is invalid", domain)
			}
			if (i == 0 || i == len(label)-1) && r == '-' {
				return fmt.Errorf("base_domain %q is invalid", domain)
			}
		}
	}
	return nil
}

func isClientConfigured(cfg appConfig) bool {
	return cfg.BaseDomain != "" && cfg.ServerAddr != "" && cfg.ServerPort > 0 && cfg.AuthToken != ""
}

func displayServiceStatus(record *serviceRecord) string {
	switch {
	case record.Status == "running" && record.PID > 0 && processExists(record.PID):
		return "running"
	case record.Status == "running":
		return "stopped"
	case record.Status == "exited" && record.LastExitCode != nil:
		return "exited(" + strconv.Itoa(*record.LastExitCode) + ")"
	case record.Status != "":
		return record.Status
	default:
		return "unknown"
	}
}

func displayProcessStatus(record *processRecord) string {
	if record == nil {
		return "not-configured"
	}
	if record.Status == "running" && record.PID > 0 && processExists(record.PID) {
		return "running"
	}
	if record.Status != "" {
		return record.Status
	}
	return "unknown"
}

func displayPID(record *processRecord) string {
	if record == nil || record.PID == 0 {
		return "-"
	}
	return strconv.Itoa(record.PID)
}

func displayProcessField(record *processRecord, field string) string {
	if record == nil {
		return "-"
	}
	switch field {
	case "ConfigPath":
		return orDefault(record.ConfigPath, "-")
	case "LogPath":
		return orDefault(record.LogPath, "-")
	default:
		return "-"
	}
}

func displayInt(value int) string {
	if value == 0 {
		return "(not set)"
	}
	return strconv.Itoa(value)
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func intPtr(v int) *int {
	return &v
}

func orDefault(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func printUsage() {
	fmt.Println(`Orion

Usage:
  orion config set base_domain edge.example.com
  orion config show
  orion server start --public-host frps.example.com # or ip
  orion server status
  orion server stop
  orion pair show
  orion pair join <token>
  orion up -n my_service -p 7000 [--http_user user --http_password password]
  orion down -n my_service
  orion serve -n my_service -p 7000 [--http_user user --http_password password] -- your_service_script
  orion list

Notes:
  frpc config is stored at ~/.orion/frpc.toml
  frps config is stored at ~/.orion/frps.toml
  bundled frpc/frps are expected next to the orion binary, or in ./bin/`)
}

func printError(err error) int {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return 1
}
