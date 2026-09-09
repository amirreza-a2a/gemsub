package textutil_test

import (
	"strings"
	"testing"

	"gemsub/internal/tester/textutil"
)

func TestNormalizeText(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "Gemini isn\u2019t currently supported in your country.",
			expected: "gemini isn't currently supported in your country.",
		},
		{
			input:    "Gemini isn&rsquo;t currently supported in your country.",
			expected: "gemini isn't currently supported in your country.",
		},
		{
			input:    "Gemini&#8217;s   new   \t\n  feature",
			expected: "gemini's new feature",
		},
		{
			input:    "\u201cNot available in your region\u201d",
			expected: "\"not available in your region\"",
		},
		{
			input:    "   Leading and trailing whitespace   \n\t  ",
			expected: "leading and trailing whitespace",
		},
		{
			input:    "Hello &amp; welcome &lt;to&gt; &quot;Gemini&#39;s&quot; world&#x2019;s best &nbsp; tool",
			expected: "hello & welcome <to> \"gemini's\" world's best tool",
		},
		{
			input:    "<style>.css { color: red; }</style>Content <!-- comment --> remains",
			expected: "content remains",
		},
	}

	for _, c := range cases {
		got := textutil.NormalizeText(c.input)
		if got != c.expected {
			t.Errorf("NormalizeText(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestNormalizeBytes(t *testing.T) {
	input := []byte("<style>body{}</style>Gemini isn&rsquo;t supported &amp; available <!-- comment --> in Iran.")
	expected := "gemini isn't supported & available in iran."
	got := textutil.NormalizeBytes(input)
	if got != expected {
		t.Errorf("NormalizeBytes() = %q; want %q", got, expected)
	}
}

func BenchmarkNormalizeText_1MB(b *testing.B) {
	snippet := "<div class=\"content\">Gemini isn\u2019t supported &amp; &#8217;available&#8217; in region \u201cXYZ\u201d.\n\t  Lots   of    spaces   and\tnewlines.\n</div>\n"
	var sb strings.Builder
	for sb.Len() < 1<<20 {
		sb.WriteString(snippet)
	}
	payload := sb.String()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = textutil.NormalizeText(payload)
	}
}

func BenchmarkNormalizeBytes_1MB(b *testing.B) {
	snippet := "<div class=\"content\">Gemini isn\u2019t supported &amp; &#8217;available&#8217; in region \u201cXYZ\u201d.\n\t  Lots   of    spaces   and\tnewlines.\n</div>\n"
	var sb strings.Builder
	for sb.Len() < 1<<20 {
		sb.WriteString(snippet)
	}
	payload := []byte(sb.String())

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = textutil.NormalizeBytes(payload)
	}
}
