package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/cli"
	"github.com/radityama/portway/internal/config"
	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/protocol"
	"github.com/radityama/portway/internal/transport"
)

var version = "dev"
var revision = "unknown"

const usage = "usage: portway [port] | start [port] | stop/status [id] | login/logout | config | list/create/delete | domain | logs | doctor | version | connect/register [--once]"

type commandOutput struct {
	json     bool
	out, err io.Writer
}

func (o commandOutput) fail(err error, code int) int {
	if o.json {
		_ = json.NewEncoder(o.out).Encode(Event{Event: "error", Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Error: err.Error()})
	} else {
		fmt.Fprintln(o.err, "error:", err)
	}
	return code
}
func (o commandOutput) result(event string, data any) int {
	if o.json {
		_ = json.NewEncoder(o.out).Encode(struct {
			Event     string `json:"event"`
			Timestamp string `json:"timestamp"`
			Data      any    `json:"data"`
		}{event, time.Now().UTC().Format(time.RFC3339Nano), data})
	} else {
		switch value := data.(type) {
		case control.Page[control.Tunnel]:
			fmt.Fprintln(o.out, "ID\tSTATUS\tNAME")
			for _, tunnel := range value.Items {
				fmt.Fprintf(o.out, "%s\t%s\t%q\n", tunnel.ID, tunnel.Status, tunnel.Name)
			}
			if value.NextCursor != nil {
				fmt.Fprintf(o.out, "Next cursor: %q\n", *value.NextCursor)
			}
			return 0
		case control.Tunnel:
			fmt.Fprintf(o.out, "%s  %s  %q\n", value.ID, value.Status, value.Name)
			return 0
		case control.DomainResult:
			fmt.Fprintf(o.out, "%s  %s  %s\n", value.Domain.ID, value.Domain.Hostname, value.Domain.Status)
			if value.Verification != nil {
				fmt.Fprintf(o.out, "TXT %s %s\n", value.Verification.Name, value.Verification.Value)
			}
			return 0
		case control.Logs:
			if !value.Available {
				fmt.Fprintln(o.out, "Recent relay observations unavailable.")
				return 0
			}
			for _, entry := range value.Logs {
				fmt.Fprintf(o.out, "%s  %s  %d  %.1fms  %s\n", entry.Timestamp.UTC().Format(time.RFC3339), entry.Method, entry.Status, entry.DurationMS, entry.Outcome)
			}
			return 0
		}
		b, _ := json.MarshalIndent(data, "", "  ")
		fmt.Fprintln(o.out, string(b))
	}
	return 0
}

// Flags are allowlisted per command, never interpolate server input or accept a
// bearer in argv. Duplicate options fail instead of silently choosing a secret.
func parseOptions(args []string) ([]string, map[string]string, error) {
	options := map[string]string{}
	var positional []string
	booleans := map[string]bool{"json": true, "once": true, "use": true, "token-stdin": true}
	values := map[string]bool{"config-dir": true, "api-url": true, "api-ca-file": true, "relay-ca-file": true, "project": true, "tunnel": true, "limit": true, "cursor": true, "name": true, "port": true, "token-file": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 8192 {
			return nil, nil, errors.New("argument is too long")
		}
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
			continue
		}
		key := strings.TrimPrefix(a, "--")
		if _, ok := options[key]; ok {
			return nil, nil, errors.New("duplicate option")
		}
		if booleans[key] {
			options[key] = "1"
			continue
		}
		if !values[key] || i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return nil, nil, errors.New("unknown option or missing value")
		}
		i++
		options[key] = args[i]
	}
	return positional, options, nil
}
func allowedOptions(options map[string]string, command string) bool {
	local := map[string]string{"connect": "once", "register": "once", "login": "token-file token-stdin", "list": "limit cursor", "create": "name port use", "logs": "limit"}
	for key := range options {
		if strings.Contains(" json config-dir api-url api-ca-file relay-ca-file project tunnel ", " "+key+" ") || strings.Contains(" "+local[command]+" ", " "+key+" ") {
			continue
		}
		return false
	}
	return true
}
func optionEnvironment(options map[string]string) map[string]string {
	flags := map[string]string{}
	for key, env := range map[string]string{"api-url": "PORTWAY_API_URL", "api-ca-file": "PORTWAY_API_CA_FILE", "relay-ca-file": "PORTWAY_RELAY_CA_FILE", "project": "PORTWAY_PROJECT_ID", "tunnel": "PORTWAY_TUNNEL_ID", "json": "PORTWAY_JSON"} {
		if value, ok := options[key]; ok {
			flags[env] = value
		}
	}
	return flags
}
func effectiveSettings(settings cli.Settings, env config.Lookup) cli.Settings {
	for key, dest := range map[string]*string{"PORTWAY_API_URL": &settings.APIURL, "PORTWAY_API_CA_FILE": &settings.APICAFile, "PORTWAY_RELAY_CA_FILE": &settings.RelayCAFile, "PORTWAY_PROJECT_ID": &settings.ProjectID, "PORTWAY_TUNNEL_ID": &settings.TunnelID, "PORTWAY_STATE_DIR": &settings.StateDir} {
		if value, ok := env(key); ok {
			*dest = value
		}
	}
	if value, ok := env("PORTWAY_LOCAL_PORT"); ok {
		var err error
		settings.LocalPort, err = strconv.Atoi(value)
		if err != nil {
			settings.LocalPort = -1
		}
	}
	return settings
}
func managementClient(env config.Lookup) (*control.Client, string, error) {
	cfg, err := config.AgentEnvironment(env)
	if err != nil {
		return nil, "", errors.New("invalid connection configuration")
	}
	if cfg.APITokenFile == "" {
		return nil, "", errors.New("login first or select a private PORTWAY_API_TOKEN_FILE")
	}
	token, err := auth.ReadTokenFile(cfg.APITokenFile)
	if err != nil {
		return nil, "", errors.New("cannot read a valid private API credential")
	}
	client, err := control.NewClient(cfg.APIURL, cfg.APICAFile, cfg.APITimeout)
	if err != nil {
		return nil, "", errors.New("invalid API destination or certificate trust")
	}
	return client, token, nil
}
func selectedID(args []string, env config.Lookup) (string, error) {
	if len(args) > 1 {
		return "", errors.New(usage)
	}
	id, _ := env("PORTWAY_TUNNEL_ID")
	if len(args) == 1 {
		id = args[0]
	}
	if !protocol.ValidTunnelID(id) {
		return "", errors.New("select a tunnel ID with an argument, --tunnel or config set tunnel_id")
	}
	return id, nil
}
func selectedProject(ctx context.Context, client *control.Client, token string, env config.Lookup) (string, error) {
	if id, ok := env("PORTWAY_PROJECT_ID"); ok && id != "" {
		if !protocol.ValidTunnelID(id) {
			return "", errors.New("invalid project ID")
		}
		return id, nil
	}
	page, err := client.Projects(ctx, token)
	if err != nil {
		return "", err
	}
	if len(page.Items) != 1 || page.NextCursor != nil {
		return "", errors.New("select a project with --project or config set project_id")
	}
	return page.Items[0].ID, nil
}
func numberOption(options map[string]string, key string, fallback, min, max int) (int, error) {
	value, ok := options[key]
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(n) != value || n < min || n > max {
		return 0, errors.New("invalid " + key)
	}
	return n, nil
}

func run(ctx context.Context, args []string, env config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		args = []string{"help"}
	}
	if len(args) == 1 && args[0] == "--version" {
		args = []string{"version"}
	}
	jsonMode, _ := env("PORTWAY_JSON")
	o := commandOutput{json: jsonMode == "1", out: stdout, err: stderr}
	for _, arg := range args {
		if arg == "--json" {
			o.json = true
		}
	}
	pos, options, err := parseOptions(args)
	if options["json"] == "1" {
		o.json = true
	}
	if err != nil {
		return o.fail(err, 2)
	}
	command := "start"
	var rest []string
	if len(pos) > 0 {
		command = pos[0]
		rest = pos[1:]
	}
	if command == "help" && len(rest) == 0 {
		return o.result("help", usage)
	}
	if !allowedOptions(options, command) {
		return o.fail(errors.New("invalid options for command"), 2)
	}
	if command == "version" {
		if len(rest) != 0 {
			return o.fail(errors.New(usage), 2)
		}
		return o.result("version", map[string]string{"version": version, "revision": revision, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH})
	}
	store, err := cli.NewStore(env, options["config-dir"])
	if err != nil {
		return o.fail(err, 1)
	}
	settings, exists, err := store.Settings()
	if err != nil {
		return o.fail(err, 1)
	}
	flags := optionEnvironment(options)
	// Configuration and logout remain usable with an expired managed session.
	if command == "config" || command == "login" || command == "logout" || command == "stop" {
		flags["PORTWAY_API_TOKEN_FILE"] = ""
	}
	lookup, err := store.Environment(env, settings, exists, flags)
	if err != nil {
		return o.fail(err, 1)
	}
	switch command {
	case "config":
		if len(rest) == 0 || len(rest) == 1 && rest[0] == "show" {
			info, _, err := store.Session()
			if err != nil {
				return o.fail(err, 1)
			}
			return o.result("config", struct {
				Settings cli.Settings     `json:"settings"`
				Session  *cli.SessionInfo `json:"session"`
			}{settings, info})
		}
		if len(rest) != 3 || rest[0] != "set" {
			return o.fail(errors.New("usage: portway config set <key> <value>"), 2)
		}
		next, err := settings.Set(rest[1], rest[2])
		if err != nil {
			return o.fail(err, 1)
		}
		if err = store.Save(next); err != nil {
			return o.fail(err, 1)
		}
		return o.result("config_saved", next)
	case "login":
		return loginCommand(ctx, rest, options, lookup, store, settings, o)
	case "logout":
		if len(rest) != 0 {
			return o.fail(errors.New(usage), 2)
		}
		info, token, err := store.ClearSession()
		if err != nil {
			return o.fail(err, 1)
		}
		if info != nil {
			client, err := control.NewClient(info.APIURL, info.APICAFile, 5*time.Second)
			if err == nil {
				defer client.Close()
				err = client.Logout(ctx, token)
			}
			if err != nil {
				return o.fail(errors.New("local session cleared; remote revocation unconfirmed (session still expires at its original deadline)"), 1)
			}
		}
		return o.result("logged_out", map[string]bool{"local_session_cleared": true, "remote_revocation_confirmed": info != nil})
	case "connect", "register":
		legacy := []string{command}
		if options["once"] == "1" {
			legacy = append(legacy, "--once")
		}
		if len(rest) != 0 {
			return o.fail(errors.New(usage), 2)
		}
		return runTunnel(ctx, legacy, lookup, stdout, stderr, nil)
	case "doctor":
		return doctorCommand(ctx, rest, lookup, o)
	case "start":
		return startCommand(ctx, rest, lookup, store, stdout, stderr, o, len(pos) == 0)
	case "stop", "status":
		id, err := selectedID(rest, lookup)
		if err != nil {
			return o.fail(err, 2)
		}
		cfg, err := config.AgentEnvironment(lookup)
		if err != nil {
			return o.fail(errors.New("invalid connection configuration"), 1)
		}
		if command == "stop" {
			if err = cli.StopLocal(ctx, cfg.StateDir, id); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					err = errors.New("no live owned local agent for this tunnel")
				}
				return o.fail(err, 1)
			}
			return o.result("stop_requested", map[string]string{"tunnel_id": id})
		}
		local, err := cli.LocalStatus(ctx, cfg.StateDir, id)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return o.fail(err, 1)
		}
		result := struct {
			Local  *cli.RuntimeStatus `json:"local"`
			Tunnel *control.Tunnel    `json:"tunnel"`
		}{}
		if err == nil {
			result.Local = &local
		}
		if cfg.APITokenFile != "" {
			client, token, err := managementClient(lookup)
			if err != nil {
				return o.fail(err, 1)
			}
			defer client.Close()
			tunnel, err := client.Tunnel(ctx, token, id)
			if err != nil {
				return o.fail(err, 1)
			}
			result.Tunnel = &tunnel
		}
		return o.result("status", result)
	case "list", "create", "delete", "domain", "logs":
		return manageCommand(ctx, command, rest, options, lookup, store, settings, o)
	default:
		if _, err := strconv.Atoi(command); err != nil {
			return o.fail(errors.New("invalid command or port; run portway help"), 1)
		}
		if len(rest) != 0 {
			return o.fail(errors.New(usage), 2)
		}
		return startCommand(ctx, []string{command}, lookup, store, stdout, stderr, o, false)
	}
}

func loginCommand(ctx context.Context, args []string, options map[string]string, env config.Lookup, store *cli.Store, settings cli.Settings, o commandOutput) int {
	if len(args) != 0 || (options["token-file"] == "") == (options["token-stdin"] == "") {
		return o.fail(errors.New("usage: portway login --token-file <private-file> | --token-stdin"), 2)
	}
	if info, _, err := store.Session(); err != nil {
		return o.fail(err, 1)
	} else if info != nil {
		return o.fail(errors.New("a session is already saved; logout first"), 1)
	}
	var token string
	var err error
	if options["token-file"] != "" {
		token, err = auth.ReadTokenFile(options["token-file"])
	} else {
		info, e := os.Stdin.Stat()
		if e != nil || info.Mode()&os.ModeCharDevice != 0 {
			return o.fail(errors.New("pipe a token into --token-stdin; interactive echo is disabled"), 1)
		}
		raw, e := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if e != nil || len(raw) > 4096 {
			err = cli.ErrState
		} else {
			token = strings.TrimSpace(string(raw))
			if !protocol.ValidToken(token) {
				err = cli.ErrState
			}
		}
	}
	if err != nil {
		return o.fail(errors.New("cannot read a valid private API key"), 1)
	}
	settings = effectiveSettings(settings, env)
	if settings.Validate() != nil {
		return o.fail(cli.ErrState, 1)
	}
	client, err := control.NewClient(settings.APIURL, settings.APICAFile, 5*time.Second)
	if err != nil {
		return o.fail(errors.New("invalid API destination or certificate trust"), 1)
	}
	defer client.Close()
	session, err := client.Login(ctx, token)
	token = ""
	if err != nil {
		return o.fail(err, 1)
	}
	if err = store.SaveSession(settings, session); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = client.Logout(cleanup, session.AccessToken)
		return o.fail(err, 1)
	}
	return o.result("logged_in", map[string]any{"api_url": settings.APIURL, "expires_at": session.ExpiresAt})
}

func manageCommand(ctx context.Context, command string, args []string, options map[string]string, env config.Lookup, store *cli.Store, settings cli.Settings, o commandOutput) int {
	client, token, err := managementClient(env)
	if err != nil {
		return o.fail(err, 1)
	}
	defer client.Close()
	switch command {
	case "list":
		if len(args) != 0 {
			return o.fail(errors.New(usage), 2)
		}
		limit, err := numberOption(options, "limit", 50, 1, 100)
		if err != nil {
			return o.fail(err, 2)
		}
		project, _ := env("PORTWAY_PROJECT_ID")
		page, err := client.Tunnels(ctx, token, project, limit, options["cursor"])
		if err != nil {
			return o.fail(err, 1)
		}
		return o.result("tunnels", page)
	case "create":
		if len(args) != 0 || options["name"] == "" {
			return o.fail(errors.New("usage: portway create --name <name> [--project <id>] [--port <port>] [--use]"), 2)
		}
		project, err := selectedProject(ctx, client, token, env)
		if err != nil {
			return o.fail(err, 1)
		}
		port, err := numberOption(options, "port", effectiveSettings(settings, env).LocalPort, 1, 65535)
		if err != nil {
			return o.fail(err, 2)
		}
		if port == 0 {
			port = 3000
		}
		tunnel, err := client.CreateTunnel(ctx, token, control.CreateTunnel{ProjectID: project, Name: options["name"], Type: "PERSISTENT", Protocol: "http", LocalHost: "127.0.0.1", LocalPort: port})
		if err != nil {
			return o.fail(err, 1)
		}
		if options["use"] == "1" {
			settings.TunnelID = tunnel.ID
			settings.ProjectID = project
			settings.LocalPort = port
			if err = store.Save(settings); err != nil {
				o.result("tunnel_created", tunnel)
				return o.fail(errors.New("tunnel created, but saving selection failed; use its ID explicitly"), 1)
			}
		}
		return o.result("tunnel_created", tunnel)
	case "delete":
		if len(args) != 1 || !protocol.ValidTunnelID(args[0]) {
			return o.fail(errors.New("usage: portway delete <tunnel-id>"), 2)
		}
		if err = client.DeleteTunnel(ctx, token, args[0]); err != nil {
			return o.fail(err, 1)
		}
		return o.result("tunnel_deleted", map[string]string{"tunnel_id": args[0]})
	case "logs":
		id, err := selectedID(args, env)
		if err != nil {
			return o.fail(err, 2)
		}
		limit, err := numberOption(options, "limit", 4, 1, 4)
		if err != nil {
			return o.fail(err, 2)
		}
		logs, err := client.Logs(ctx, token, id, limit)
		if err != nil {
			return o.fail(err, 1)
		}
		return o.result("logs", logs)
	case "domain":
		if len(args) != 2 {
			return o.fail(errors.New("usage: portway domain add <hostname> | verify/activate/challenge/remove <id>"), 2)
		}
		var result control.DomainResult
		if args[0] == "add" {
			id, err := selectedID(nil, env)
			if err != nil {
				return o.fail(err, 2)
			}
			result, err = client.AddDomain(ctx, token, id, args[1])
			if err != nil {
				return o.fail(err, 1)
			}
		} else {
			if !strings.Contains(" verify activate challenge remove ", " "+args[0]+" ") {
				return o.fail(errors.New("unknown domain action"), 2)
			}
			result, err = client.DomainAction(ctx, token, args[1], args[0])
			if err != nil {
				return o.fail(err, 1)
			}
		}
		if args[0] == "remove" {
			return o.result("domain_removed", map[string]string{"domain_id": args[1]})
		}
		return o.result("domain_"+args[0], result)
	}
	return o.fail(errors.New(usage), 2)
}

func startCommand(ctx context.Context, args []string, env config.Lookup, store *cli.Store, stdout, stderr io.Writer, o commandOutput, implicit bool) int {
	if len(args) > 1 {
		return o.fail(errors.New(usage), 2)
	}
	port := 0
	if len(args) == 1 {
		var err error
		port, err = strconv.Atoi(args[0])
		if err != nil || port < 1 || port > 65535 {
			return o.fail(errors.New("invalid port"), 1)
		}
	} else {
		dir, err := os.Getwd()
		if err != nil {
			return o.fail(errors.New("cannot inspect local project"), 1)
		}
		detection, err := cli.Detect(ctx, env, dir)
		if err != nil {
			code := 1
			if implicit {
				code = 2
			}
			return o.fail(err, code)
		}
		port = detection.Port
	}
	if !cli.Reachable(ctx, port) {
		return o.fail(fmt.Errorf("localhost:%d is not reachable", port), 1)
	}
	cfg, err := config.AgentEnvironment(env)
	if err != nil {
		return o.fail(errors.New("invalid connection configuration"), 1)
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	// Only the managed-session workflow allocates automatically. External-key
	// diagnostics keep the documented legacy tnl_local_dev default.
	selected, _ := env("PORTWAY_TUNNEL_ID")
	if cfg.APITokenFile == store.TokenPath() && selected == "" {
		client, token, err := managementClient(env)
		if err != nil {
			return o.fail(err, 1)
		}
		defer client.Close()
		project, err := selectedProject(ctx, client, token, env)
		if err != nil {
			return o.fail(err, 1)
		}
		tunnel, err := client.CreateTunnel(ctx, token, control.CreateTunnel{ProjectID: project, Name: "CLI development", Type: "EPHEMERAL", Protocol: "http", LocalHost: "127.0.0.1", LocalPort: port})
		if err != nil {
			return o.fail(err, 1)
		}
		original := env
		env = func(key string) (string, bool) {
			if key == "PORTWAY_TUNNEL_ID" {
				return tunnel.ID, true
			}
			return original(key)
		}
		cfg.TunnelID = tunnel.ID
		defer func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			if client.DeleteTunnel(cleanup, token, tunnel.ID) != nil {
				o.result("ephemeral_cleanup_unconfirmed", map[string]string{"tunnel_id": tunnel.ID, "message": "delete this tunnel after API access returns"})
			}
		}()
	}
	local, err := cli.StartRuntime(child, cfg.StateDir, cfg.TunnelID, port, cancel)
	if err != nil {
		return o.fail(err, 1)
	}
	defer local.Close()
	return runTunnel(child, []string{strconv.Itoa(port)}, env, stdout, stderr, local)
}

type finding struct {
	Check   string `json:"check"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func doctorCommand(ctx context.Context, args []string, env config.Lookup, o commandOutput) int {
	if len(args) > 1 {
		return o.fail(errors.New(usage), 2)
	}
	var findings []finding
	add := func(check string, ok bool, message string) { findings = append(findings, finding{check, ok, message}) }
	cfg, err := config.AgentEnvironment(env)
	if err != nil {
		add("configuration", false, "check connection environment and saved settings")
	} else {
		add("configuration", true, "connection settings are valid")
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		address, name := cfg.Address, cfg.ServerName
		if cfg.APITokenFile != "" {
			client, token, err := managementClient(env)
			add("credential", err == nil, "private API credential availability")
			if err == nil {
				defer client.Close()
				_, err = client.Me(ctx, token)
				add("api_identity", err == nil, "API authentication and identity")
				err = client.Readiness(ctx, token)
				add("api_ready", err == nil, "API storage and presence readiness")
				page, err := client.Relays(ctx, token)
				found := false
				if err == nil {
					for _, relay := range page.Items {
						if relay.Status == "HEALTHY" {
							address = net.JoinHostPort(relay.Hostname, strconv.Itoa(relay.Port))
							name = relay.Hostname
							found = true
							break
						}
					}
				}
				add("relay_assignment", found, "healthy relay metadata (no credential allocated)")
				if !found {
					address = ""
				}
			}
		} else {
			_, err := auth.ReadTokenFile(cfg.TokenFile)
			add("credential", err == nil, "private direct credential file format (relay authentication is checked by connect)")
		}
		if address != "" {
			trust, err := transport.ClientConfig(cfg.CAFile, name)
			if err == nil {
				dialer := transport.TLSDialer{Config: trust, Timeout: cfg.ConnectTimeout}
				conn, e := dialer.Dial(ctx, address)
				err = e
				if conn != nil {
					transport.Close(conn)
				}
			}
			add("relay_tls", err == nil, "verified relay TLS 1.3 and portway/1 reachability")
		}
		if len(args) == 1 {
			port, e := strconv.Atoi(args[0])
			if e != nil || port < 1 || port > 65535 {
				return o.fail(errors.New("invalid port"), 2)
			}
			add("local_service", cli.Reachable(ctx, port), "explicit loopback service")
		}
	}
	ok := true
	for _, f := range findings {
		ok = ok && f.OK
	}
	o.result("doctor", struct {
		OK     bool      `json:"ok"`
		Checks []finding `json:"checks"`
	}{ok, findings})
	if !ok {
		return 1
	}
	return 0
}
