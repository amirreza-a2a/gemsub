// Package parser turns share links (vless://, vmess://, trojan://,
// ss://, hysteria2://, hy2://, tuic://) into sing-box outbound option
// structs that internal/tester can dial through directly, without ever
// shelling out to an external sing-box process.
package parser

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
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
	case strings.HasPrefix(link, "hysteria2://"), strings.HasPrefix(link, "hy2://"):
		outbound, warnings, err = parseHysteria2(link)
	case strings.HasPrefix(link, "tuic://"):
		outbound, warnings, err = parseTUIC(link)
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

// --- hysteria2 / hy2 ---

func parseHysteria2(link string) (option.Outbound, []string, error) {
	var prefix string
	if strings.HasPrefix(link, "hysteria2://") {
		prefix = "hysteria2://"
	} else if strings.HasPrefix(link, "hy2://") {
		prefix = "hy2://"
	} else {
		return option.Outbound{}, nil, fmt.Errorf("unsupported scheme in %q", link)
	}

	userinfo, host, portPart, rawQuery, err := splitURI(link, prefix)
	if err != nil {
		return option.Outbound{}, nil, fmt.Errorf("hysteria2: %w", err)
	}

	rawQuery = strings.ReplaceAll(rawQuery, "&amp;", "&")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return option.Outbound{}, nil, fmt.Errorf("hysteria2: query: %w", err)
	}

	password := userinfo
	if password == "" {
		password = q.Get("auth")
	}

	opts := option.Hysteria2OutboundOptions{
		ServerOptions: option.ServerOptions{
			Server: host,
		},
		Password: password,
	}

	mport := q.Get("mport")
	if mport == "" {
		mport = q.Get("ports")
	}

	if mport != "" {
		ports, err := parsePortHopping(mport)
		if err != nil {
			return option.Outbound{}, nil, fmt.Errorf("hysteria2: mport: %w", err)
		}
		opts.ServerPorts = badoption.Listable[string](ports)
	} else if portPart != "" {
		if strings.Contains(portPart, ",") || strings.Contains(portPart, "-") || strings.Contains(portPart, ":") {
			ports, err := parsePortHopping(portPart)
			if err != nil {
				return option.Outbound{}, nil, fmt.Errorf("hysteria2: port: %w", err)
			}
			opts.ServerPorts = badoption.Listable[string](ports)
		} else {
			p, err := strconv.ParseUint(portPart, 10, 16)
			if err != nil || p == 0 {
				return option.Outbound{}, nil, fmt.Errorf("hysteria2: invalid port %q", portPart)
			}
			opts.ServerPort = uint16(p)
		}
	} else {
		opts.ServerPort = 443
	}

	obfs := q.Get("obfs")
	obfsPassword := q.Get("obfs-password")
	if obfs != "" || obfsPassword != "" {
		if obfs == "" {
			return option.Outbound{}, nil, fmt.Errorf("hysteria2: missing obfs type")
		}
		if obfsPassword == "" {
			return option.Outbound{}, nil, fmt.Errorf("hysteria2: missing obfs-password")
		}
		switch obfs {
		case "salamander", "gecko":
			opts.Obfs = &option.Hysteria2Obfs{
				Type:     obfs,
				Password: obfsPassword,
			}
		default:
			return option.Outbound{}, nil, fmt.Errorf("hysteria2: unsupported obfs type %q", obfs)
		}
	}

	sni := q.Get("sni")
	if sni == "" {
		sni = host
	}

	insecure := false
	insecureVal := q.Get("insecure")
	if insecureVal == "" {
		insecureVal = q.Get("allow_insecure")
	}
	if insecureVal == "" {
		insecureVal = q.Get("allowInsecure")
	}
	if insecureVal != "" {
		b, err := parseBoolParam(insecureVal)
		if err != nil {
			return option.Outbound{}, nil, fmt.Errorf("hysteria2: insecure: %w", err)
		}
		insecure = b
	}

	tlsOpts := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: sni,
		Insecure:   insecure,
	}

	if pin := q.Get("pinSHA256"); pin != "" {
		var rawPin []byte
		pinClean := strings.TrimSpace(pin)
		if b, err := hex.DecodeString(pinClean); err == nil && len(b) == 32 {
			rawPin = b
		} else if b, err := base64.StdEncoding.DecodeString(pinClean); err == nil && len(b) == 32 {
			rawPin = b
		} else if b, err := base64.RawStdEncoding.DecodeString(pinClean); err == nil && len(b) == 32 {
			rawPin = b
		} else {
			if b, err := hex.DecodeString(pinClean); err == nil {
				rawPin = b
			} else {
				return option.Outbound{}, nil, fmt.Errorf("hysteria2: invalid pinSHA256 %q", pin)
			}
		}
		tlsOpts.CertificatePublicKeySHA256 = badoption.Listable[[]byte]{rawPin}
	}

	if alpnStr := q.Get("alpn"); alpnStr != "" {
		var alpnList []string
		for _, a := range strings.Split(alpnStr, ",") {
			if a = strings.TrimSpace(a); a != "" {
				alpnList = append(alpnList, a)
			}
		}
		if len(alpnList) > 0 {
			tlsOpts.ALPN = badoption.Listable[string](alpnList)
		}
	}

	opts.TLS = tlsOpts

	return option.Outbound{
		Type:    "hysteria2",
		Options: &opts,
	}, nil, nil
}

// --- tuic ---

func parseTUIC(link string) (option.Outbound, []string, error) {
	if !strings.HasPrefix(link, "tuic://") {
		return option.Outbound{}, nil, fmt.Errorf("unsupported scheme in %q", link)
	}

	userinfo, host, portPart, rawQuery, err := splitURI(link, "tuic://")
	if err != nil {
		return option.Outbound{}, nil, fmt.Errorf("tuic: %w", err)
	}

	parts := strings.SplitN(userinfo, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return option.Outbound{}, nil, fmt.Errorf("tuic: invalid userinfo: expected <uuid>:<password>")
	}
	rawUUID, password := parts[0], parts[1]

	parsedUUID, err := uuid.FromString(rawUUID)
	if err != nil {
		return option.Outbound{}, nil, fmt.Errorf("tuic: invalid userinfo: expected <uuid>:<password>")
	}

	if portPart == "" {
		return option.Outbound{}, nil, fmt.Errorf("tuic: missing port in %q", link)
	}
	port, err := strconv.ParseUint(portPart, 10, 16)
	if err != nil || port == 0 {
		return option.Outbound{}, nil, fmt.Errorf("tuic: invalid port %q", portPart)
	}

	rawQuery = strings.ReplaceAll(rawQuery, "&amp;", "&")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return option.Outbound{}, nil, fmt.Errorf("tuic: query: %w", err)
	}

	opts := option.TUICOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     host,
			ServerPort: uint16(port),
		},
		UUID:     parsedUUID.String(),
		Password: password,
	}

	cc := q.Get("congestion_control")
	if cc == "" {
		cc = q.Get("congestion-controller")
	}
	if cc != "" {
		switch strings.ToLower(cc) {
		case "bbr", "cubic", "new_reno":
			opts.CongestionControl = strings.ToLower(cc)
		default:
			return option.Outbound{}, nil, fmt.Errorf("tuic: invalid congestion_control %q", cc)
		}
	}

	udpRelay := q.Get("udp_relay_mode")
	if udpRelay == "" {
		udpRelay = q.Get("udp-relay-mode")
	}
	if udpRelay != "" {
		switch strings.ToLower(udpRelay) {
		case "native", "quic":
			opts.UDPRelayMode = strings.ToLower(udpRelay)
		default:
			return option.Outbound{}, nil, fmt.Errorf("tuic: invalid udp_relay_mode %q", udpRelay)
		}
	}

	udpStream := q.Get("udp_over_stream")
	if udpStream == "" {
		udpStream = q.Get("udp-over-stream")
	}
	if udpStream != "" {
		b, err := parseBoolParam(udpStream)
		if err != nil {
			return option.Outbound{}, nil, fmt.Errorf("tuic: udp_over_stream: %w", err)
		}
		opts.UDPOverStream = b
	}
	if opts.UDPOverStream && opts.UDPRelayMode != "" {
		return option.Outbound{}, nil, fmt.Errorf("tuic: udp_over_stream conflicts with udp_relay_mode")
	}

	zeroRTT := q.Get("zero_rtt_handshake")
	if zeroRTT == "" {
		zeroRTT = q.Get("reduce_rtt")
	}
	if zeroRTT == "" {
		zeroRTT = q.Get("zero-rtt-handshake")
	}
	if zeroRTT != "" {
		b, err := parseBoolParam(zeroRTT)
		if err != nil {
			return option.Outbound{}, nil, fmt.Errorf("tuic: zero_rtt_handshake: %w", err)
		}
		opts.ZeroRTTHandshake = b
	}

	hb := q.Get("heartbeat")
	if hb == "" {
		hb = q.Get("heartbeat-interval")
	}
	if hb != "" {
		d, err := time.ParseDuration(hb)
		if err != nil {
			if sec, errSec := strconv.Atoi(hb); errSec == nil && sec > 0 {
				d = time.Duration(sec) * time.Second
			} else {
				return option.Outbound{}, nil, fmt.Errorf("tuic: invalid heartbeat %q: %w", hb, err)
			}
		}
		opts.Heartbeat = badoption.Duration(d)
	}

	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	disableSNI := q.Get("disable_sni")
	if disableSNI == "" {
		disableSNI = q.Get("disable-sni")
	}
	if disableSNI != "" {
		b, err := parseBoolParam(disableSNI)
		if err != nil {
			return option.Outbound{}, nil, fmt.Errorf("tuic: disable_sni: %w", err)
		}
		if b {
			sni = ""
		} else if sni == "" {
			sni = host
		}
	} else if sni == "" {
		sni = host
	}

	insecure := false
	insecureVal := q.Get("allow_insecure")
	if insecureVal == "" {
		insecureVal = q.Get("insecure")
	}
	if insecureVal == "" {
		insecureVal = q.Get("allowInsecure")
	}
	if insecureVal == "" {
		insecureVal = q.Get("skip-cert-verify")
	}
	if insecureVal != "" {
		b, err := parseBoolParam(insecureVal)
		if err != nil {
			return option.Outbound{}, nil, fmt.Errorf("tuic: allow_insecure: %w", err)
		}
		insecure = b
	}

	alpnList := []string{"h3"}
	if alpnStr := q.Get("alpn"); alpnStr != "" {
		var parsedALPN []string
		for _, a := range strings.Split(alpnStr, ",") {
			if a = strings.TrimSpace(a); a != "" {
				parsedALPN = append(parsedALPN, a)
			}
		}
		if len(parsedALPN) > 0 {
			alpnList = parsedALPN
		}
	}

	opts.TLS = &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: sni,
		Insecure:   insecure,
		ALPN:       badoption.Listable[string](alpnList),
	}

	return option.Outbound{
		Type:    "tuic",
		Options: &opts,
	}, nil, nil
}

// splitURI splits a URI without relying on url.Parse's host-port digit checks,
// handling IPv6 brackets, multi-port ranges, userinfo, and trailing query/fragment.
func splitURI(link, prefix string) (userinfo, host, portPart, rawQuery string, err error) {
	raw := strings.TrimPrefix(link, prefix)
	if i := strings.Index(raw, "#"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "?"); i >= 0 {
		rawQuery = raw[i+1:]
		raw = raw[:i]
	}
	raw = strings.TrimSuffix(raw, "/")

	if at := strings.LastIndex(raw, "@"); at >= 0 {
		userinfo = raw[:at]
		raw = raw[at+1:]
	}

	if unescaped, err := url.PathUnescape(userinfo); err == nil {
		userinfo = unescaped
	}

	if strings.HasPrefix(raw, "[") {
		endBracket := strings.Index(raw, "]")
		if endBracket < 0 {
			return "", "", "", "", fmt.Errorf("malformed ipv6 address in %q", raw)
		}
		host = raw[1:endBracket]
		rest := raw[endBracket+1:]
		if strings.HasPrefix(rest, ":") {
			portPart = rest[1:]
		} else if rest != "" {
			return "", "", "", "", fmt.Errorf("malformed authority %q", raw)
		}
	} else {
		colon := strings.Index(raw, ":")
		if colon >= 0 {
			host = raw[:colon]
			portPart = raw[colon+1:]
		} else {
			host = raw
		}
	}

	if host == "" {
		return "", "", "", "", fmt.Errorf("missing host in %q", link)
	}

	return userinfo, host, portPart, rawQuery, nil
}

func parseBoolParam(val string) (bool, error) {
	switch strings.ToLower(val) {
	case "1", "true", "yes":
		return true, nil
	case "0", "false", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value %q", val)
	}
}

func parsePortHopping(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("bad port range: %q", part)
			}
			start, err1 := strconv.ParseUint(strings.TrimSpace(rangeParts[0]), 10, 16)
			end, err2 := strconv.ParseUint(strings.TrimSpace(rangeParts[1]), 10, 16)
			if err1 != nil || err2 != nil || start == 0 || end == 0 || start > end {
				return nil, fmt.Errorf("bad port range: %q", part)
			}
			out = append(out, fmt.Sprintf("%d:%d", start, end))
		} else if strings.Contains(part, ":") {
			rangeParts := strings.Split(part, ":")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("bad port range: %q", part)
			}
			start, err1 := strconv.ParseUint(strings.TrimSpace(rangeParts[0]), 10, 16)
			end, err2 := strconv.ParseUint(strings.TrimSpace(rangeParts[1]), 10, 16)
			if err1 != nil || err2 != nil || start == 0 || end == 0 || start > end {
				return nil, fmt.Errorf("bad port range: %q", part)
			}
			out = append(out, fmt.Sprintf("%d:%d", start, end))
		} else {
			p, err := strconv.ParseUint(part, 10, 16)
			if err != nil || p == 0 {
				return nil, fmt.Errorf("bad port: %q", part)
			}
			out = append(out, fmt.Sprintf("%d", p))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty port list")
	}
	return out, nil
}
