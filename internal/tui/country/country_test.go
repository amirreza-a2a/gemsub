package country_test

import (
	"testing"

	"gemsub/internal/tui/country"
)

func TestFormatCountry_Unicode(t *testing.T) {
	tests := []struct {
		code string
		name string
		want string
	}{
		{"DE", "Germany", "🇩🇪 Germany"},
		{"FR", "France", "🇫🇷 France"},
		{"US", "United States", "🇺🇸 United States"},
		{"GB", "United Kingdom", "🇬🇧 United Kingdom"},
		{"UK", "United Kingdom", "🇬🇧 United Kingdom"},
		{"IR", "Iran", "🇮🇷 Iran"},
		{"RU", "Russia", "🇷🇺 Russia"},
		{"JP", "Japan", "🇯🇵 Japan"},
		{"NL", "Netherlands", "🇳🇱 Netherlands"},
		{"SG", "Singapore", "🇸🇬 Singapore"},
	}

	for _, tt := range tests {
		t.Run(tt.code+"_"+tt.name, func(t *testing.T) {
			got := country.FormatCountry(tt.code, tt.name, country.ModeUnicode)
			if got != tt.want {
				t.Errorf("FormatCountry(%q, %q, ModeUnicode) = %q, want %q", tt.code, tt.name, got, tt.want)
			}
		})
	}
}

func TestFormatCountry_ASCII(t *testing.T) {
	tests := []struct {
		code string
		name string
		want string
	}{
		{"DE", "Germany", "[DE] Germany"},
		{"FR", "France", "[FR] France"},
		{"US", "United States", "[US] United States"},
		{"GB", "United Kingdom", "[GB] United Kingdom"},
		{"UK", "United Kingdom", "[UK] United Kingdom"},
		{"IR", "Iran", "[IR] Iran"},
		{"RU", "Russia", "[RU] Russia"},
		{"JP", "Japan", "[JP] Japan"},
		{"NL", "Netherlands", "[NL] Netherlands"},
		{"SG", "Singapore", "[SG] Singapore"},
	}

	for _, tt := range tests {
		t.Run(tt.code+"_"+tt.name, func(t *testing.T) {
			got := country.FormatCountry(tt.code, tt.name, country.ModeASCII)
			if got != tt.want {
				t.Errorf("FormatCountry(%q, %q, ModeASCII) = %q, want %q", tt.code, tt.name, got, tt.want)
			}
		})
	}
}

func TestFormatCountry_AutoResolution(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantMode country.Mode
		wantText string
	}{
		{
			name: "SSH session forces ASCII fallback",
			env: map[string]string{
				"SSH_TTY":      "/dev/pts/1",
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"LANG":         "en_US.UTF-8",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "SSH client forces ASCII fallback",
			env: map[string]string{
				"SSH_CLIENT": "192.168.1.50 54321 22",
				"TERM":       "xterm-256color",
				"LANG":       "en_US.UTF-8",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "Minimal Linux console forces ASCII fallback",
			env: map[string]string{
				"TERM": "linux",
				"LANG": "en_US.UTF-8",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "Dumb terminal forces ASCII fallback",
			env: map[string]string{
				"TERM": "dumb",
				"LANG": "en_US.UTF-8",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "Non-UTF8 locale forces ASCII fallback",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"LANG":         "C",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "POSIX precedence: LC_ALL=C overrides UTF-8 LANG",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"LC_ALL":       "C",
				"LANG":         "en_US.UTF-8",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "POSIX precedence: LC_ALL=POSIX overrides UTF-8 LANG",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"LC_ALL":       "POSIX",
				"LANG":         "en_US.UTF-8",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "POSIX precedence: LC_CTYPE=C overrides UTF-8 LANG when LC_ALL is unset",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"LC_CTYPE":     "C",
				"LANG":         "en_US.UTF-8",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "POSIX precedence: LC_ALL=UTF-8 takes precedence over non-UTF8 LANG",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"LC_ALL":       "en_US.UTF-8",
				"LC_CTYPE":     "C",
				"LANG":         "C",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeUnicode,
			wantText: "🇩🇪 Germany",
		},
		{
			name: "POSIX precedence: LC_CTYPE=UTF-8 takes precedence over non-UTF8 LANG when LC_ALL unset",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"LC_CTYPE":     "en_US.UTF-8",
				"LANG":         "C",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeUnicode,
			wantText: "🇩🇪 Germany",
		},
		{
			name: "All locale variables unset forces ASCII fallback",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"COLORTERM":    "truecolor",
				"TERM_PROGRAM": "iTerm.app",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "Generic xterm-256color without verified emulator conservatively falls back to ASCII",
			env: map[string]string{
				"TERM":      "xterm-256color",
				"COLORTERM": "truecolor",
				"LANG":      "en_US.UTF-8",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
		{
			name: "Modern terminal iTerm with UTF-8 resolves to Unicode",
			env: map[string]string{
				"TERM":         "xterm-256color",
				"TERM_PROGRAM": "iTerm.app",
				"LANG":         "en_US.UTF-8",
			},
			wantMode: country.ModeUnicode,
			wantText: "🇩🇪 Germany",
		},
		{
			name: "Modern terminal ghostty with UTF-8 resolves to Unicode",
			env: map[string]string{
				"TERM":         "xterm-ghostty",
				"TERM_PROGRAM": "ghostty",
				"LANG":         "en_US.UTF-8",
			},
			wantMode: country.ModeUnicode,
			wantText: "🇩🇪 Germany",
		},
		{
			name: "Windows Terminal with UTF-8 resolves to Unicode",
			env: map[string]string{
				"TERM":       "xterm-256color",
				"WT_SESSION": "some-guid",
				"LANG":       "en_US.UTF-8",
			},
			wantMode: country.ModeUnicode,
			wantText: "🇩🇪 Germany",
		},
		{
			name: "Explicit GEMSUB_FLAG_MODE=unicode overrides conservative auto",
			env: map[string]string{
				"TERM":             "xterm-256color",
				"LANG":             "en_US.UTF-8",
				"GEMSUB_FLAG_MODE": "unicode",
			},
			wantMode: country.ModeUnicode,
			wantText: "🇩🇪 Germany",
		},
		{
			name: "Explicit GEMSUB_FLAG_MODE=ascii overrides modern terminal",
			env: map[string]string{
				"TERM_PROGRAM":     "iTerm.app",
				"LANG":             "en_US.UTF-8",
				"GEMSUB_FLAG_MODE": "ascii",
			},
			wantMode: country.ModeASCII,
			wantText: "[DE] Germany",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(key string) string {
				return tt.env[key]
			}
			resolved := country.ResolveModeWithLookup(country.ModeAuto, lookup)
			if resolved != tt.wantMode {
				t.Errorf("ResolveModeWithLookup(ModeAuto) = %v, want %v", resolved, tt.wantMode)
			}
			got := country.FormatCountryWithLookup("DE", "Germany", country.ModeAuto, lookup)
			if got != tt.wantText {
				t.Errorf("FormatCountryWithLookup(DE, Germany) = %q, want %q", got, tt.wantText)
			}
		})
	}
}

func TestFormatCountry_InvalidAndEmptyInputs(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		countryName string
		mode        country.Mode
		want        string
	}{
		{
			name:        "Both empty",
			code:        "",
			countryName: "",
			mode:        country.ModeUnicode,
			want:        "",
		},
		{
			name:        "Empty code with name in Unicode mode",
			code:        "",
			countryName: "Germany",
			mode:        country.ModeUnicode,
			want:        "Germany",
		},
		{
			name:        "Empty code with name in ASCII mode",
			code:        "",
			countryName: "Germany",
			mode:        country.ModeASCII,
			want:        "Germany",
		},
		{
			name:        "Valid code with empty name in Unicode mode",
			code:        "DE",
			countryName: "",
			mode:        country.ModeUnicode,
			want:        "🇩🇪",
		},
		{
			name:        "Valid code with empty name in ASCII mode",
			code:        "DE",
			countryName: "",
			mode:        country.ModeASCII,
			want:        "[DE]",
		},
		{
			name:        "Lowercase code normalized in Unicode mode",
			code:        "de",
			countryName: "Germany",
			mode:        country.ModeUnicode,
			want:        "🇩🇪 Germany",
		},
		{
			name:        "Lowercase code normalized in ASCII mode",
			code:        "de",
			countryName: "Germany",
			mode:        country.ModeASCII,
			want:        "[DE] Germany",
		},
		{
			name:        "1-character invalid code with name",
			code:        "D",
			countryName: "Germany",
			mode:        country.ModeASCII,
			want:        "[D] Germany",
		},
		{
			name:        "3-character invalid code with name",
			code:        "DEU",
			countryName: "Germany",
			mode:        country.ModeASCII,
			want:        "[DEU] Germany",
		},
		{
			name:        "Numeric code with name",
			code:        "12",
			countryName: "Test",
			mode:        country.ModeASCII,
			want:        "[12] Test",
		},
		{
			name:        "Special characters code with name",
			code:        "!@",
			countryName: "Test",
			mode:        country.ModeUnicode,
			want:        "[!@] Test",
		},
		{
			name:        "Numeric code without name",
			code:        "99",
			countryName: "",
			mode:        country.ModeASCII,
			want:        "[99]",
		},
		{
			name:        "Invalid 2-letter code ZZ in Unicode mode must not produce flag",
			code:        "ZZ",
			countryName: "Nowhere",
			mode:        country.ModeUnicode,
			want:        "[ZZ] Nowhere",
		},
		{
			name:        "Invalid 2-letter code ZZ in ASCII mode",
			code:        "ZZ",
			countryName: "Nowhere",
			mode:        country.ModeASCII,
			want:        "[ZZ] Nowhere",
		},
		{
			name:        "Invalid 2-letter code XX without name in Unicode mode",
			code:        "XX",
			countryName: "",
			mode:        country.ModeUnicode,
			want:        "[XX]",
		},
		{
			name:        "Invalid 2-letter code XX without name in ASCII mode",
			code:        "XX",
			countryName: "",
			mode:        country.ModeASCII,
			want:        "[XX]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := country.FormatCountry(tt.code, tt.countryName, tt.mode)
			if got != tt.want {
				t.Errorf("FormatCountry(%q, %q, %v) = %q, want %q", tt.code, tt.countryName, tt.mode, got, tt.want)
			}
		})
	}
}

func TestFormatRemark(t *testing.T) {
	t.Run("ASCII mode converts emoji flags to ASCII brackets", func(t *testing.T) {
		cases := []struct {
			input string
			want  string
		}{
			{"🇩🇪 Germany", "[DE] Germany"},
			{"🇩🇪 Germany 01", "[DE] Germany 01"},
			{"🇫🇷 France", "[FR] France"},
			{"🇺🇸 US - New York", "[US] US - New York"},
			{"🇩🇪 🇫🇷 🇺🇸", "[DE] [FR] [US]"},
			{"No flag here", "No flag here"},
			{"[ZZ] Nowhere", "[ZZ] Nowhere"},
			{"[OK] Node", "[OK] Node"},
			{"", ""},
		}

		for _, tc := range cases {
			got := country.FormatRemark(tc.input, country.ModeASCII)
			if got != tc.want {
				t.Errorf("FormatRemark(%q, ModeASCII) = %q, want %q", tc.input, got, tc.want)
			}
		}
	})

	t.Run("Unicode mode converts bracketed country codes to emoji flags", func(t *testing.T) {
		cases := []struct {
			input string
			want  string
		}{
			{"[DE] Germany", "🇩🇪 Germany"},
			{"[FR] France", "🇫🇷 France"},
			{"[US] United States", "🇺🇸 United States"},
			{"[GB] United Kingdom", "🇬🇧 United Kingdom"},
			{"[UK] London", "🇬🇧 London"},
			{"🇩🇪 Germany", "🇩🇪 Germany"},           // preserves existing
			{"[ZZ] Nowhere", "[ZZ] Nowhere"},       // invalid country code not converted
			{"[XX] Node", "[XX] Node"},             // invalid country code not converted
			{"[SERVER-1] Test", "[SERVER-1] Test"}, // not a country code
			{"[OK] Node", "[OK] Node"},             // not a country code
			{"Normal remark", "Normal remark"},
			{"", ""},
		}

		for _, tc := range cases {
			got := country.FormatRemark(tc.input, country.ModeUnicode)
			if got != tc.want {
				t.Errorf("FormatRemark(%q, ModeUnicode) = %q, want %q", tc.input, got, tc.want)
			}
		}
	})
}

func TestCountryCode_Validity(t *testing.T) {
	validCodes := []struct {
		code     string
		wantFlag string
		wantAsc  string
	}{
		{"DE", "🇩🇪", "[DE]"},
		{"FR", "🇫🇷", "[FR]"},
		{"IR", "🇮🇷", "[IR]"},
		{"US", "🇺🇸", "[US]"},
		{"UK", "🇬🇧", "[UK]"},
		{"de", "🇩🇪", "[DE]"},
		{"fr", "🇫🇷", "[FR]"},
		{"ir", "🇮🇷", "[IR]"},
		{"uk", "🇬🇧", "[UK]"},
	}

	for _, tc := range validCodes {
		if !country.IsValidCountryCode(tc.code) {
			t.Errorf("expected %q to be valid country code", tc.code)
		}
		flag, ok := country.CountryCodeToFlag(tc.code)
		if !ok || flag != tc.wantFlag {
			t.Errorf("CountryCodeToFlag(%q) = (%q, %v), want (%q, true)", tc.code, flag, ok, tc.wantFlag)
		}
		ascii, ok := country.CountryCodeToASCII(tc.code)
		if !ok || ascii != tc.wantAsc {
			t.Errorf("CountryCodeToASCII(%q) = (%q, %v), want (%q, true)", tc.code, ascii, ok, tc.wantAsc)
		}
	}

	invalidCodes := []string{"ZZ", "XX", "AA", "OO", "12", "D", "DEU", "!@", ""}
	for _, code := range invalidCodes {
		if country.IsValidCountryCode(code) {
			t.Errorf("expected %q to be invalid country code", code)
		}
		if flag, ok := country.CountryCodeToFlag(code); ok || flag != "" {
			t.Errorf("CountryCodeToFlag(%q) = (%q, %v), want empty and false", code, flag, ok)
		}
		if ascii, ok := country.CountryCodeToASCII(code); ok || ascii != "" {
			t.Errorf("CountryCodeToASCII(%q) = (%q, %v), want empty and false", code, ascii, ok)
		}
	}
}

func TestFormatFlag(t *testing.T) {
	if got := country.FormatFlag("DE", country.ModeUnicode); got != "🇩🇪" {
		t.Errorf("FormatFlag(DE, ModeUnicode) = %q, want 🇩🇪", got)
	}
	if got := country.FormatFlag("DE", country.ModeASCII); got != "[DE]" {
		t.Errorf("FormatFlag(DE, ModeASCII) = %q, want [DE]", got)
	}
}
