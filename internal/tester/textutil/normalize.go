// Package textutil provides shared text normalization utilities for HTML content
// processing. It is a leaf package with no dependencies on tester, gemini, or
// transport packages, enabling safe import from any sibling package.
package textutil

import (
	"bytes"
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxInspectBytes bounds payload inspection to 1MB.
const MaxInspectBytes = 1 << 20

// NormalizeText unescapes HTML entities, normalizes quotes/apostrophes,
// collapses whitespace, and converts to lowercase using a streaming single-pass scanner.
func NormalizeText(s string) string {
	if len(s) == 0 {
		return ""
	}
	if len(s) > MaxInspectBytes {
		s = s[:MaxInspectBytes]
	}

	var b strings.Builder
	b.Grow(len(s))

	inWhitespace := false
	for i := 0; i < len(s); {
		// Skip <style>...</style> non-content segments
		if i+6 <= len(s) && (s[i] == '<' && (s[i+1] == 's' || s[i+1] == 'S') && (s[i+2] == 't' || s[i+2] == 'T') && (s[i+3] == 'y' || s[i+3] == 'Y') && (s[i+4] == 'l' || s[i+4] == 'L') && (s[i+5] == 'e' || s[i+5] == 'E')) {
			endIdx := IndexCaseInsensitiveString(s[i+6:], "</style>")
			if endIdx >= 0 {
				i += 6 + endIdx + 8
				continue
			}
		}

		// Skip <!-- ... --> comments
		if i+4 <= len(s) && s[i] == '<' && s[i+1] == '!' && s[i+2] == '-' && s[i+3] == '-' {
			endIdx := strings.Index(s[i+4:], "-->")
			if endIdx >= 0 {
				i += 4 + endIdx + 3
				continue
			}
		}

		if s[i] == '&' {
			semi := strings.IndexByte(s[i:], ';')
			if semi > 1 && semi <= 32 && !strings.ContainsAny(s[i:i+semi], " \t\r\n<>") {
				entity := s[i : i+semi+1]
				if handled := HandleEntity(&b, entity, &inWhitespace); handled {
					i += semi + 1
					continue
				}
				unescaped := html.UnescapeString(entity)
				if unescaped != entity {
					for _, ur := range unescaped {
						ProcessRune(&b, ur, &inWhitespace)
					}
					i += semi + 1
					continue
				}
			}
		}

		var r rune
		var size int
		if s[i] < utf8.RuneSelf {
			r = rune(s[i])
			size = 1
		} else {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		i += size

		ProcessRune(&b, r, &inWhitespace)
	}

	return b.String()
}

// NormalizeBytes normalizes byte slices directly to avoid heap string allocations,
// unescaping HTML entities, collapsing whitespace, converting to lowercase, and
// bounding inspection by skipping non-content segments.
func NormalizeBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > MaxInspectBytes {
		b = b[:MaxInspectBytes]
	}

	var builder strings.Builder
	builder.Grow(len(b))

	inWhitespace := false
	for i := 0; i < len(b); {
		// Skip <style>...</style> non-content segments
		if i+6 <= len(b) && (b[i] == '<' && (b[i+1] == 's' || b[i+1] == 'S') && (b[i+2] == 't' || b[i+2] == 'T') && (b[i+3] == 'y' || b[i+3] == 'Y') && (b[i+4] == 'l' || b[i+4] == 'L') && (b[i+5] == 'e' || b[i+5] == 'E')) {
			endIdx := IndexCaseInsensitiveBytes(b[i+6:], []byte("</style>"))
			if endIdx >= 0 {
				i += 6 + endIdx + 8
				continue
			}
		}

		// Skip <!-- ... --> comments
		if i+4 <= len(b) && b[i] == '<' && b[i+1] == '!' && b[i+2] == '-' && b[i+3] == '-' {
			endIdx := bytes.Index(b[i+4:], []byte("-->"))
			if endIdx >= 0 {
				i += 4 + endIdx + 3
				continue
			}
		}

		if b[i] == '&' {
			semi := bytes.IndexByte(b[i:], ';')
			if semi > 1 && semi <= 32 && !bytes.ContainsAny(b[i:i+semi], " \t\r\n<>") {
				entity := string(b[i : i+semi+1])
				if handled := HandleEntity(&builder, entity, &inWhitespace); handled {
					i += semi + 1
					continue
				}
				unescaped := html.UnescapeString(entity)
				if unescaped != entity {
					for _, ur := range unescaped {
						ProcessRune(&builder, ur, &inWhitespace)
					}
					i += semi + 1
					continue
				}
			}
		}

		var r rune
		var size int
		if b[i] < utf8.RuneSelf {
			r = rune(b[i])
			size = 1
		} else {
			r, size = utf8.DecodeRune(b[i:])
		}
		i += size

		ProcessRune(&builder, r, &inWhitespace)
	}

	return builder.String()
}

// IndexCaseInsensitiveBytes returns the index of the first case-insensitive occurrence
// of target in b, or -1 if not found.
func IndexCaseInsensitiveBytes(b []byte, target []byte) int {
	if len(target) == 0 {
		return 0
	}
	if len(b) < len(target) {
		return -1
	}
	max := len(b) - len(target)
	firstLower := target[0] | 0x20
	for i := 0; i <= max; i++ {
		if (b[i] | 0x20) == firstLower {
			match := true
			for j := 1; j < len(target); j++ {
				if (b[i+j] | 0x20) != (target[j] | 0x20) {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
	}
	return -1
}

// IndexCaseInsensitiveString returns the index of the first case-insensitive occurrence
// of target in s, or -1 if not found.
func IndexCaseInsensitiveString(s string, target string) int {
	if len(target) == 0 {
		return 0
	}
	if len(s) < len(target) {
		return -1
	}
	max := len(s) - len(target)
	firstLower := target[0] | 0x20
	for i := 0; i <= max; i++ {
		if (s[i] | 0x20) == firstLower {
			match := true
			for j := 1; j < len(target); j++ {
				if (s[i+j] | 0x20) != (target[j] | 0x20) {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
	}
	return -1
}

// ProcessRune normalizes a single rune: smart quotes → ASCII, NBSP → space,
// collapses whitespace, and lowercases.
func ProcessRune(b *strings.Builder, r rune, inWhitespace *bool) {
	switch r {
	case '\u2018', '\u2019', '\u02BC', '`', '\u00B4':
		r = '\''
	case '\u201C', '\u201D':
		r = '"'
	case '\u00a0':
		r = ' '
	}

	if unicode.IsSpace(r) {
		if !*inWhitespace && b.Len() > 0 {
			*inWhitespace = true
		}
		return
	}

	if *inWhitespace {
		b.WriteByte(' ')
		*inWhitespace = false
	}

	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte(byte(r + ('a' - 'A')))
		} else {
			b.WriteByte(byte(r))
		}
	} else {
		b.WriteRune(unicode.ToLower(r))
	}
}

// HandleEntity handles common HTML entities directly without going through
// the full html.UnescapeString path.
func HandleEntity(b *strings.Builder, entity string, inWhitespace *bool) bool {
	switch entity {
	case "&rsquo;", "&lsquo;", "&apos;":
		ProcessRune(b, '\'', inWhitespace)
		return true
	case "&quot;", "&ldquo;", "&rdquo;":
		ProcessRune(b, '"', inWhitespace)
		return true
	case "&nbsp;":
		ProcessRune(b, ' ', inWhitespace)
		return true
	case "&amp;":
		ProcessRune(b, '&', inWhitespace)
		return true
	case "&lt;":
		ProcessRune(b, '<', inWhitespace)
		return true
	case "&gt;":
		ProcessRune(b, '>', inWhitespace)
		return true
	}
	if len(entity) > 3 && entity[1] == '#' {
		var cp rune
		if entity[2] == 'x' || entity[2] == 'X' {
			for _, ch := range entity[3 : len(entity)-1] {
				if '0' <= ch && ch <= '9' {
					cp = cp*16 + rune(ch-'0')
				} else if 'a' <= ch && ch <= 'f' {
					cp = cp*16 + rune(ch-'a'+10)
				} else if 'A' <= ch && ch <= 'F' {
					cp = cp*16 + rune(ch-'A'+10)
				} else {
					return false
				}
			}
		} else {
			for _, ch := range entity[2 : len(entity)-1] {
				if '0' <= ch && ch <= '9' {
					cp = cp*10 + rune(ch-'0')
				} else {
					return false
				}
			}
		}
		if cp > 0 && utf8.ValidRune(cp) {
			ProcessRune(b, cp, inWhitespace)
			return true
		}
	}
	return false
}
