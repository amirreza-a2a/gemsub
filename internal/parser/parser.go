// Package parser turns share links (vless://, vmess://, trojan://,
// ss://) into sing-box outbound option structs that internal/tester
// can dial through directly, without ever shelling out to an
// external sing-box process.
//
// NOTE: this file depends on github.com/sagernet/sing-box/option,
// which this sandbox cannot fetch (its dependency tree pulls in
// golang.org/x/... packages from a host not reachable here). The
// field names below match the option package's public API as
// documented at https://sing-box.sagernet.org/configuration/outbound/
// but have not been compiled against the real module. Run
// `go mod tidy && go build ./...` after fetching dependencies and
// fix any field-name drift against your installed sing-box version
// before relying on this.
package parser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

// Candidate pairs the original link (used as the map key everywhere
// else in the system, and as what gets served back to Throne) with
// the parsed outbound options ready to hand to the tester.
type Candidate struct {
	Link     string
	Outbound option.Outbound
	Warnings []string
}

// Parse dispatches on scheme and returns a ready-to-test Candidate.
func Parse(link string) (Candidate, error) {
	link = strings.TrimSpace(link)

	var outbound option.Outbound
	var warnings []string
	var err error

	switch {
	case strings.HasPrefix(link, "vmess://"):
		outbound, err = parseVMess(link)
	case strings.HasPrefix(link, "vless://"):
		outbound, warnings, err = parseURIStyle(link, "vless")
	case strings.HasPrefix(link, "trojan://"):
		outbound, warnings, err = parseURIStyle(link, "trojan")
	case strings.HasPrefix(link, "ss://"):
		outbound, err = parseShadowsocks(link)
	default:
		return Candidate{}, fmt.Errorf("unsupported scheme in %q", link)
	}
	if err != nil {
		return Candidate{}, err
	}

	outbound.Tag = "probe"
	return Candidate{Link: link, Outbound: outbound, Warnings: warnings}, nil
}

// --- vmess ---

type vmessJSON struct {
	Add  string `json:"add"`
	Port string `json:"port"` // sometimes a number, sometimes a string in the wild
	ID   string `json:"id"`
	Aid  string `json:"aid"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
}

func parseVMess(link string) (option.Outbound, error) {
	raw := strings.TrimPrefix(link, "vmess://")
	decoded, err := b64Decode(raw)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("vmess: base64: %w", err)
	}

	var v vmessJSON
	if err := json.Unmarshal([]byte(decoded), &v); err != nil {
		return option.Outbound{}, fmt.Errorf("vmess: json: %w", err)
	}

	port, err := strconv.Atoi(v.Port)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("vmess: port: %w", err)
	}
	aid, _ := strconv.Atoi(v.Aid) // defaults to 0 on parse failure, which is the common case

	opts := option.VMessOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     v.Add,
			ServerPort: uint16(port),
		},
		UUID:     v.ID,
		Security: "auto",
		AlterId:  aid,
	}

	if transport := buildTransport(v.Net, v.Path, v.Host); transport != nil {
		opts.Transport = transport
	}
	if strings.EqualFold(v.TLS, "tls") {
		sni := v.SNI
		if sni == "" {
			sni = v.Host
		}
		if sni == "" {
			sni = v.Add
		}
		opts.TLS = &option.OutboundTLSOptions{
			Enabled:    true,
			ServerName: sni,
		}
	}

	return option.Outbound{
		Type:    "vmess",
		Options: &opts,
	}, nil
}

// --- vless / trojan (both URI-style: scheme://user@host:port?params#remark) ---

func parseURIStyle(link, scheme string) (option.Outbound, []string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return option.Outbound{}, nil, fmt.Errorf("%s: %w", scheme, err)
	}

	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return option.Outbound{}, nil, fmt.Errorf("%s: missing host or port", scheme)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return option.Outbound{}, nil, fmt.Errorf("%s: port: %w", scheme, err)
	}

	userinfo := ""
	if u.User != nil {
		userinfo = u.User.Username()
	}

	q := u.Query()
	server := option.ServerOptions{Server: host, ServerPort: uint16(port)}
	tls, warnings := buildTLS(q, host)
	transport := buildTransport(q.Get("type"), q.Get("path"), q.Get("host"))

	switch scheme {
	case "vless":
		opts := option.VLESSOutboundOptions{
			ServerOptions: server,
			UUID:          userinfo,
			Flow:          q.Get("flow"),
			Transport:     transport,
		}
		opts.TLS = tls

		return option.Outbound{
			Type:    "vless",
			Options: &opts,
		}, warnings, nil

	case "trojan":
		opts := option.TrojanOutboundOptions{
			ServerOptions: server,
			Password:      userinfo,
			Transport:     transport,
		}
		opts.TLS = tls

		return option.Outbound{
			Type:    "trojan",
			Options: &opts,
		}, warnings, nil
	}

	return option.Outbound{}, nil, fmt.Errorf("unreachable scheme %q", scheme)
}

func buildTLS(q url.Values, host string) (*option.OutboundTLSOptions, []string) {
	security := strings.ToLower(q.Get("security"))
	if security != "tls" && security != "reality" {
		return nil, nil
	}

	sni := q.Get("sni")
	if sni == "" {
		sni = host
	}

	tls := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: sni,
	}

	var warnings []string

	fp := q.Get("fp")
	if fp != "" {
		if strings.EqualFold(fp, "unsafe") || strings.EqualFold(fp, "random") {
			warnings = append(warnings, fmt.Sprintf("reality/tls: unsupported fp=%s, normalized uTLS fingerprint to chrome", fp))
			fp = "chrome"
		}
		tls.UTLS = &option.OutboundUTLSOptions{
			Enabled:     true,
			Fingerprint: fp,
		}
	}

	if security == "reality" {
		if tls.UTLS == nil {
			tls.UTLS = &option.OutboundUTLSOptions{
				Enabled:     true,
				Fingerprint: "chrome",
			}
			warnings = append(warnings, "reality: missing fp query parameter, defaulted uTLS fingerprint to chrome")
		}
		tls.Reality = &option.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: q.Get("pbk"),
			ShortID:   q.Get("sid"),
		}
	}

	return tls, warnings
}

// --- shadowsocks ---

func parseShadowsocks(link string) (option.Outbound, error) {
	raw := strings.TrimPrefix(link, "ss://")
	if i := strings.Index(raw, "#"); i >= 0 {
		raw = raw[:i]
	}

	var method, password, host, portStr string

	if at := strings.LastIndex(raw, "@"); at >= 0 {
		creds := raw[:at]
		hostport := cleanHostPort(raw[at+1:])

		if decoded, err := b64Decode(creds); err == nil && strings.Contains(decoded, ":") {
			creds = decoded
		}
		parts := strings.SplitN(creds, ":", 2)
		if len(parts) != 2 {
			return option.Outbound{}, fmt.Errorf("ss: invalid credentials")
		}
		method, password = parts[0], parts[1]

		h, p, err := splitHostPort(hostport)
		if err != nil {
			return option.Outbound{}, fmt.Errorf("ss: %w", err)
		}
		host, portStr = h, p
	} else {
		decoded, err := b64Decode(cleanHostPort(raw))
		if err != nil {
			return option.Outbound{}, fmt.Errorf("ss: base64: %w", err)
		}
		at := strings.LastIndex(decoded, "@")
		if at < 0 {
			return option.Outbound{}, fmt.Errorf("ss: malformed legacy link")
		}
		creds := decoded[:at]
		parts := strings.SplitN(creds, ":", 2)
		if len(parts) != 2 {
			return option.Outbound{}, fmt.Errorf("ss: invalid credentials")
		}
		method, password = parts[0], parts[1]

		h, p, err := splitHostPort(decoded[at+1:])
		if err != nil {
			return option.Outbound{}, fmt.Errorf("ss: %w", err)
		}
		host, portStr = h, p
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("ss: port: %w", err)
	}

	opts := option.ShadowsocksOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     host,
			ServerPort: uint16(port),
		},
		Method:   method,
		Password: password,
	}

	return option.Outbound{
		Type:    "shadowsocks",
		Options: &opts,
	}, nil
}

// --- shared helpers ---

func buildTransport(netType, path, host string) *option.V2RayTransportOptions {
	switch strings.ToLower(netType) {
	case "", "tcp", "raw":
		return nil

	case "ws":
		if path == "" {
			path = "/"
		}

		opts := &option.V2RayWebsocketOptions{
			Path: path,
		}

		if host != "" {
			opts.Headers = badoption.HTTPHeader{
				"Host": badoption.Listable[string]{host},
			}
		}

		return &option.V2RayTransportOptions{
			Type:             "ws",
			WebsocketOptions: *opts,
		}

	case "grpc":
		return &option.V2RayTransportOptions{
			Type: "grpc",
			GRPCOptions: option.V2RayGRPCOptions{
				ServiceName: path,
			},
		}

	case "http", "h2":
		opts := option.V2RayHTTPOptions{
			Path: path,
		}

		if host != "" {
			opts.Host = badoption.Listable[string]{host}
		}

		return &option.V2RayTransportOptions{
			Type:        "http",
			HTTPOptions: opts,
		}

	default:
		return nil
	}
}

// cleanHostPort strips a trailing "?query" or "/path" that some
// subscription generators append after host:port.
func cleanHostPort(s string) string {
	if i := strings.IndexAny(s, "?/"); i >= 0 {
		s = s[:i]
	}
	return s
}

func splitHostPort(hostport string) (host, port string, err error) {
	hostport = cleanHostPort(hostport)
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return "", "", fmt.Errorf("missing port in %q", hostport)
	}
	return hostport[:i], hostport[i+1:], nil
}

func b64Decode(s string) (string, error) {
	s = strings.TrimSpace(s)
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return string(data), nil
	}
	data, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
