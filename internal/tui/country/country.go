package country

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Mode represents the presentation strategy for country flags.
type Mode string

const (
	// ModeAuto automatically determines flag presentation using deterministic,
	// conservative capability heuristics (falling back to ASCII on SSH, minimal
	// Linux consoles, or terminals without verified emoji support).
	ModeAuto Mode = "auto"

	// ModeUnicode forces Unicode regional indicator emoji presentation (e.g. 🇩🇪 Germany).
	ModeUnicode Mode = "unicode"

	// ModeASCII forces portable ASCII bracketed country code presentation (e.g. [DE] Germany).
	ModeASCII Mode = "ascii"
)

var bracketedCountryRegex = regexp.MustCompile(`\[([A-Za-z]{2})\]`)

// ISO 3166-1 alpha-2 official country codes plus common user-assigned aliases (UK).
var validCountryCodes = map[string]bool{
	"AD": true, "AE": true, "AF": true, "AG": true, "AI": true, "AL": true, "AM": true, "AO": true,
	"AQ": true, "AR": true, "AS": true, "AT": true, "AU": true, "AW": true, "AX": true, "AZ": true,
	"BA": true, "BB": true, "BD": true, "BE": true, "BF": true, "BG": true, "BH": true, "BI": true,
	"BJ": true, "BL": true, "BM": true, "BN": true, "BO": true, "BQ": true, "BR": true, "BS": true,
	"BT": true, "BV": true, "BW": true, "BY": true, "BZ": true, "CA": true, "CC": true, "CD": true,
	"CF": true, "CG": true, "CH": true, "CI": true, "CK": true, "CL": true, "CM": true, "CN": true,
	"CO": true, "CR": true, "CU": true, "CV": true, "CW": true, "CX": true, "CY": true, "CZ": true,
	"DE": true, "DJ": true, "DK": true, "DM": true, "DO": true, "DZ": true, "EC": true, "EE": true,
	"EG": true, "EH": true, "ER": true, "ES": true, "ET": true, "FI": true, "FJ": true, "FK": true,
	"FM": true, "FO": true, "FR": true, "GA": true, "GB": true, "GD": true, "GE": true, "GF": true,
	"GG": true, "GH": true, "GI": true, "GL": true, "GM": true, "GN": true, "GP": true, "GQ": true,
	"GR": true, "GS": true, "GT": true, "GU": true, "GW": true, "GY": true, "HK": true, "HM": true,
	"HN": true, "HR": true, "HT": true, "HU": true, "ID": true, "IE": true, "IL": true, "IM": true,
	"IN": true, "IO": true, "IQ": true, "IR": true, "IS": true, "IT": true, "JE": true, "JM": true,
	"JO": true, "JP": true, "KE": true, "KG": true, "KH": true, "KI": true, "KM": true, "KN": true,
	"KP": true, "KR": true, "KW": true, "KY": true, "KZ": true, "LA": true, "LB": true, "LC": true,
	"LI": true, "LK": true, "LR": true, "LS": true, "LT": true, "LU": true, "LV": true, "LY": true,
	"MA": true, "MC": true, "MD": true, "ME": true, "MF": true, "MG": true, "MH": true, "MK": true,
	"ML": true, "MM": true, "MN": true, "MO": true, "MP": true, "MQ": true, "MR": true, "MS": true,
	"MT": true, "MU": true, "MV": true, "MW": true, "MX": true, "MY": true, "MZ": true, "NA": true,
	"NC": true, "NE": true, "NF": true, "NG": true, "NI": true, "NL": true, "NO": true, "NP": true,
	"NR": true, "NU": true, "NZ": true, "OM": true, "PA": true, "PE": true, "PF": true, "PG": true,
	"PH": true, "PK": true, "PL": true, "PM": true, "PN": true, "PR": true, "PS": true, "PT": true,
	"PW": true, "PY": true, "QA": true, "RE": true, "RO": true, "RS": true, "RU": true, "RW": true,
	"SA": true, "SB": true, "SC": true, "SD": true, "SE": true, "SG": true, "SH": true, "SI": true,
	"SJ": true, "SK": true, "SL": true, "SM": true, "SN": true, "SO": true, "SR": true, "SS": true,
	"ST": true, "SV": true, "SX": true, "SY": true, "SZ": true, "TC": true, "TD": true, "TF": true,
	"TG": true, "TH": true, "TJ": true, "TK": true, "TL": true, "TM": true, "TN": true, "TO": true,
	"TR": true, "TT": true, "TV": true, "TW": true, "TZ": true, "UA": true, "UG": true, "UM": true,
	"US": true, "UY": true, "UZ": true, "VA": true, "VC": true, "VE": true, "VG": true, "VI": true,
	"VN": true, "VU": true, "WF": true, "WS": true, "YE": true, "YT": true, "ZA": true, "ZM": true,
	"ZW": true, "UK": true,
}

// IsValidCountryCode returns true if code is a recognized 2-letter ISO 3166-1 alpha-2 code.
func IsValidCountryCode(code string) bool {
	return validCountryCodes[strings.ToUpper(strings.TrimSpace(code))]
}

// CountryCodeToFlag converts a 2-letter country code to Unicode regional indicator emoji flag runes.
// e.g. "DE" -> "🇩🇪", "FR" -> "🇫🇷", "US" -> "🇺🇸".
func CountryCodeToFlag(code string) (string, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 2 {
		return "", false
	}
	c1 := code[0]
	c2 := code[1]
	if !isAlpha(c1) || !isAlpha(c2) {
		return "", false
	}
	u1 := toUpper(c1)
	u2 := toUpper(c2)
	upper := string([]byte{u1, u2})
	if !validCountryCodes[upper] {
		return "", false
	}

	// Map UK to GB for Unicode regional indicator sequence
	if u1 == 'U' && u2 == 'K' {
		u1, u2 = 'G', 'B'
	}

	r1 := rune(0x1F1E6 + int(u1-'A'))
	r2 := rune(0x1F1E6 + int(u2-'A'))
	return string([]rune{r1, r2}), true
}

// CountryCodeToASCII formats a 2-letter country code as a bracketed ASCII string e.g. "DE" -> "[DE]".
func CountryCodeToASCII(code string) (string, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 2 {
		return "", false
	}
	c1 := code[0]
	c2 := code[1]
	if !isAlpha(c1) || !isAlpha(c2) {
		return "", false
	}
	u1 := toUpper(c1)
	u2 := toUpper(c2)
	upper := string([]byte{u1, u2})
	if !validCountryCodes[upper] {
		return "", false
	}
	return fmt.Sprintf("[%c%c]", u1, u2), true
}

// FlagToCountryCode converts a 2-rune Unicode regional indicator flag to its 2-letter uppercase ASCII code.
func FlagToCountryCode(flag string) (string, bool) {
	runes := []rune(flag)
	if len(runes) != 2 {
		return "", false
	}
	r1, r2 := runes[0], runes[1]
	if r1 < 0x1F1E6 || r1 > 0x1F1FF || r2 < 0x1F1E6 || r2 > 0x1F1FF {
		return "", false
	}
	c1 := byte('A' + (r1 - 0x1F1E6))
	c2 := byte('A' + (r2 - 0x1F1E6))
	code := string([]byte{c1, c2})
	if !validCountryCodes[code] {
		return "", false
	}
	return code, true
}

// ResolveModeWithLookup resolves a presentation Mode to either ModeUnicode or ModeASCII
// using conservative, deterministic heuristics and the provided environment lookup function.
func ResolveModeWithLookup(mode Mode, lookupEnv func(string) string) Mode {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}

	switch mode {
	case ModeUnicode:
		return ModeUnicode
	case ModeASCII:
		return ModeASCII
	case ModeAuto, "":
		// 1. Explicit environment override takes precedence
		if envMode := strings.ToLower(strings.TrimSpace(lookupEnv("GEMSUB_FLAG_MODE"))); envMode != "" {
			switch envMode {
			case string(ModeUnicode):
				return ModeUnicode
			case string(ModeASCII):
				return ModeASCII
			}
		}

		// 2. SSH session detection: remote client terminal cannot be verified.
		// Conservative fallback to ASCII ensures readability across remote connections.
		if lookupEnv("SSH_CLIENT") != "" || lookupEnv("SSH_TTY") != "" || lookupEnv("SSH_CONNECTION") != "" {
			return ModeASCII
		}

		// 3. Minimal Linux console or dumb terminal environments
		term := strings.ToLower(strings.TrimSpace(lookupEnv("TERM")))
		if term == "" || term == "dumb" || term == "linux" || term == "vt100" || term == "vt220" || term == "cons25" {
			return ModeASCII
		}

		// 4. Non-UTF-8 locale check
		if !isUTF8Locale(lookupEnv) {
			return ModeASCII
		}

		// 5. Check for modern terminal emulators known to reliably support Unicode emoji flag presentation.
		// We deliberately avoid assuming generic TERM=xterm-256color or fc-match proves flag support.
		termProgram := strings.ToLower(strings.TrimSpace(lookupEnv("TERM_PROGRAM")))
		switch termProgram {
		case "iterm.app", "apple_terminal", "wezterm", "ghostty":
			return ModeUnicode
		}
		if lookupEnv("WT_SESSION") != "" { // Windows Terminal
			return ModeUnicode
		}
		if lookupEnv("KITTY_WINDOW_ID") != "" || term == "xterm-kitty" { // Kitty
			return ModeUnicode
		}
		if term == "wezterm" || term == "xterm-ghostty" {
			return ModeUnicode
		}

		// Conservative default: fallback to ASCII to guarantee readability on all other terminals
		return ModeASCII
	default:
		return ModeASCII
	}
}

// ResolveMode resolves mode using the actual process environment.
func ResolveMode(mode Mode) Mode {
	return ResolveModeWithLookup(mode, os.Getenv)
}

func isUTF8Locale(lookupEnv func(string) string) bool {
	var effective string
	if val := lookupEnv("LC_ALL"); val != "" {
		effective = val
	} else if val := lookupEnv("LC_CTYPE"); val != "" {
		effective = val
	} else {
		effective = lookupEnv("LANG")
	}

	if effective == "" {
		return false
	}
	upper := strings.ToUpper(effective)
	return strings.Contains(upper, "UTF-8") || strings.Contains(upper, "UTF8")
}

// FormatCountryWithLookup formats country code and country name according to mode using the lookupEnv function.
func FormatCountryWithLookup(code, name string, mode Mode, lookupEnv func(string) string) string {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)

	effectiveMode := ResolveModeWithLookup(mode, lookupEnv)

	if code == "" {
		return name
	}

	if effectiveMode == ModeUnicode {
		if flag, ok := CountryCodeToFlag(code); ok {
			if name != "" {
				return flag + " " + name
			}
			return flag
		}
	} else {
		if ascii, ok := CountryCodeToASCII(code); ok {
			if name != "" {
				return ascii + " " + name
			}
			return ascii
		}
	}

	// Invalid / non-2-letter country code fallback
	if name != "" {
		return fmt.Sprintf("[%s] %s", code, name)
	}
	return fmt.Sprintf("[%s]", code)
}

// FormatCountry formats country code and country name according to mode.
// Preferred Unicode: "🇩🇪 Germany"
// Portable fallback: "[DE] Germany"
func FormatCountry(code, name string, mode Mode) string {
	return FormatCountryWithLookup(code, name, mode, os.Getenv)
}

// FormatFlag returns either the Unicode flag emoji or ASCII bracketed code according to mode.
func FormatFlag(code string, mode Mode) string {
	return FormatCountry(code, "", mode)
}

// FormatRemarkWithLookup formats a candidate remark string according to mode using lookupEnv.
func FormatRemarkWithLookup(remark string, mode Mode, lookupEnv func(string) string) string {
	if remark == "" {
		return ""
	}

	effectiveMode := ResolveModeWithLookup(mode, lookupEnv)
	if effectiveMode == ModeASCII {
		return ReplaceFlagsWithASCII(remark)
	}
	return ReplaceASCIIWithFlags(remark)
}

// FormatRemark formats a candidate remark string according to mode.
// In ASCII mode, any Unicode regional indicator emoji flags are converted to portable "[XX]" codes.
// In Unicode mode, recognizable bracketed country codes like "[DE]" are converted to "🇩🇪".
func FormatRemark(remark string, mode Mode) string {
	return FormatRemarkWithLookup(remark, mode, os.Getenv)
}

// ReplaceFlagsWithASCII scans string for 2-rune Unicode regional indicator flag emojis
// and converts each pair into a bracketed ASCII country code like "[DE]".
func ReplaceFlagsWithASCII(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.Grow(len(s))

	for i := 0; i < len(runes); {
		r := runes[i]
		if r >= 0x1F1E6 && r <= 0x1F1FF && i+1 < len(runes) && runes[i+1] >= 0x1F1E6 && runes[i+1] <= 0x1F1FF {
			c1 := byte('A' + (r - 0x1F1E6))
			c2 := byte('A' + (runes[i+1] - 0x1F1E6))
			code := string([]byte{c1, c2})
			if validCountryCodes[code] {
				sb.WriteByte('[')
				sb.WriteByte(c1)
				sb.WriteByte(c2)
				sb.WriteByte(']')
				i += 2
				continue
			}
		}
		sb.WriteRune(r)
		i++
	}

	return sb.String()
}

// ReplaceASCIIWithFlags replaces recognized bracketed ISO 3166-1 country codes (e.g. "[DE]")
// with their Unicode regional indicator emoji flags (e.g. "🇩🇪").
func ReplaceASCIIWithFlags(s string) string {
	return bracketedCountryRegex.ReplaceAllStringFunc(s, func(m string) string {
		code := strings.ToUpper(m[1:3])
		if validCountryCodes[code] {
			if flag, ok := CountryCodeToFlag(code); ok {
				return flag
			}
		}
		return m
	})
}

func isAlpha(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func toUpper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - ('a' - 'A')
	}
	return b
}
