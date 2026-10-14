package classifiercore_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"gemsub/internal/store"
	"gemsub/internal/tester/classifiercore"
	"gemsub/internal/tester/gemini"
	"gemsub/internal/tester/ruleengine"
)

func TestGeminiPatternsParityWithLegacy(t *testing.T) {
	// 1. Locate and parse internal/tester/gemini/probe.go directly via AST
	candidates := []string{
		filepath.Join("..", "gemini", "probe.go"),
		filepath.Join("internal", "tester", "gemini", "probe.go"),
	}
	var probePath string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			probePath = c
			break
		}
	}
	if probePath == "" {
		t.Fatalf("could not find gemini/probe.go in paths: %v", candidates)
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, probePath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", probePath, err)
	}

	var countryCodePattern, locationBlockPattern string
	var restrictedCountries []string

	ast.Inspect(node, func(n ast.Node) bool {
		switch decl := n.(type) {
		case *ast.ValueSpec:
			for i, name := range decl.Names {
				switch name.Name {
				case "countryCodeRegex":
					if i < len(decl.Values) {
						if call, ok := decl.Values[i].(*ast.CallExpr); ok && len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								s, err := strconv.Unquote(lit.Value)
								if err == nil {
									countryCodePattern = s
								}
							}
						}
					}
				case "locationBlockRegex":
					if i < len(decl.Values) {
						if call, ok := decl.Values[i].(*ast.CallExpr); ok && len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								s, err := strconv.Unquote(lit.Value)
								if err == nil {
									locationBlockPattern = s
								}
							}
						}
					}
				case "restrictedCountries":
					if i < len(decl.Values) {
						if comp, ok := decl.Values[i].(*ast.CompositeLit); ok {
							for _, elt := range comp.Elts {
								if kve, ok := elt.(*ast.KeyValueExpr); ok {
									if lit, ok := kve.Key.(*ast.BasicLit); ok {
										s, err := strconv.Unquote(lit.Value)
										if err == nil {
											restrictedCountries = append(restrictedCountries, s)
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return true
	})

	if countryCodePattern == "" {
		t.Fatal("failed to extract countryCodeRegex from probe.go")
	}
	if locationBlockPattern == "" {
		t.Fatal("failed to extract locationBlockRegex from probe.go")
	}
	if len(restrictedCountries) == 0 {
		t.Fatal("failed to extract restrictedCountries from probe.go")
	}

	// 2. Assert exact verbatim string match between probe.go and classifiercore exports
	if countryCodePattern != classifiercore.GeminiCountryCodePattern {
		t.Errorf("countryCodeRegex in probe.go (%q) does not match classifiercore.GeminiCountryCodePattern (%q)",
			countryCodePattern, classifiercore.GeminiCountryCodePattern)
	}

	if locationBlockPattern != classifiercore.GeminiLocationBlockPattern {
		t.Errorf("locationBlockRegex in probe.go (%q) does not match classifiercore.GeminiLocationBlockPattern (%q)",
			locationBlockPattern, classifiercore.GeminiLocationBlockPattern)
	}

	slices.Sort(restrictedCountries)
	classifiercoreCountries := slices.Clone(classifiercore.GeminiRestrictedCountries)
	slices.Sort(classifiercoreCountries)

	if !slices.Equal(restrictedCountries, classifiercoreCountries) {
		t.Errorf("restrictedCountries in probe.go (%v) does not match classifiercore.GeminiRestrictedCountries (%v)",
			restrictedCountries, classifiercoreCountries)
	}

	// 3. Behavioral parity test: verify identical verdict outcomes between
	// legacy gemini.ClassifyResponse and ruleengine using classifiercore exports
	geminiRS, err := ruleengine.NewRuleSet(ruleengine.RuleSetConfig{
		Name: "gemini-parity",
		Mode: ruleengine.ModeStrict,
		Rules: []ruleengine.Rule{
			{
				Phase: "rejection",
				Condition: ruleengine.NewLeafCondition(
					ruleengine.RawBodySignal(),
					ruleengine.MustRegexExtractIn(classifiercore.GeminiCountryCodePattern, classifiercore.GeminiRestrictedCountries),
				),
				Verdict: ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "restricted country detected in WIZ_global_data"},
			},
			{
				Phase: "rejection",
				Condition: ruleengine.NewLeafCondition(
					ruleengine.RawBodySignal(),
					ruleengine.MustRegex(classifiercore.GeminiLocationBlockPattern),
				),
				Verdict: ruleengine.Verdict{Status: store.StatusFailed, Category: store.ErrRegionBlocked, Reason: "gemini restriction flag detected"},
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to construct rule set: %v", err)
	}

	resp := &http.Response{StatusCode: 200, Header: http.Header{}}

	// Test 3a: Restricted country in WIZ_global_data
	bodyBlockedCountry := []byte(`<script>window.WIZ_global_data = {"vXmutd":"%.@.\"IR\",\"ZZ\""};</script>`)
	legacyResCountry := gemini.ClassifyResponse(resp, bodyBlockedCountry, nil)
	ctxCountry := ruleengine.BuildEvaluationContext(resp, bodyBlockedCountry, 0)
	engineResCountry := ruleengine.Evaluate(ctxCountry, geminiRS)

	if legacyResCountry.Category != store.ErrRegionBlocked || engineResCountry.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked for country block, legacy=%v engine=%v",
			legacyResCountry.Category, engineResCountry.Category)
	}

	// Test 3b: Location rejection flag
	bodyBlockedLocation := []byte(`var config = {"status":"LOCATION_REJECTED"};`)
	legacyResLocation := gemini.ClassifyResponse(resp, bodyBlockedLocation, nil)
	ctxLocation := ruleengine.BuildEvaluationContext(resp, bodyBlockedLocation, 0)
	engineResLocation := ruleengine.Evaluate(ctxLocation, geminiRS)

	if legacyResLocation.Category != store.ErrRegionBlocked || engineResLocation.Category != store.ErrRegionBlocked {
		t.Errorf("expected ErrRegionBlocked for location block, legacy=%v engine=%v",
			legacyResLocation.Category, engineResLocation.Category)
	}
}
