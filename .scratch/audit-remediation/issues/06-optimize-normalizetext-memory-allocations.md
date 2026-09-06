# 06: perf(classifier): optimize NormalizeText memory allocations

Type: task
Status: resolved
Blocked by: None (can start immediately)

## Question

How do we eliminate massive heap allocations when classifying candidate responses on 1MB payloads?

## What to build

Refactor text normalization in `internal/tester/classifier.go` to avoid `strings.Fields` word-token array allocations across 1MB HTML bodies, bounding text inspection to relevant document segments and using streaming/byte-level scanning.

## Acceptance criteria

- [x] `NormalizeText` allocations reduced significantly on large HTML bodies.
- [x] Classification precision maintained across all existing classifier test fixtures.
- [x] Benchmark verifies reduction in allocated bytes per probe.

## Answer

1. Refactored `NormalizeText` and introduced `NormalizeBytes` in `internal/tester/classifier.go` using a streaming, single-pass byte-level scanner writing to a preallocated `strings.Builder`.
2. Eliminated multiple intermediate string allocations (`strings.ReplaceAll`, `strings.Fields` word-token array slices, `strings.Join`, and `strings.ToLower`), collapsing whitespace, mapping quotes/apostrophes, and lowercasing in-place during scanning.
3. Added fast-path zero-allocation decoding for common named entities (`&rsquo;`, `&quot;`, `&nbsp;`, `&amp;`, etc.) and decimal/hexadecimal numeric entities (`&#...;`), avoiding full-document unescaping copies.
4. Bounded text inspection to relevant document segments: capped inspection at 1MB (`maxInspectBytes`), skipped non-content segments (`<style>...</style>` stylesheets and `<!-- ... -->` comments), and extracted `<title>` directly from the byte stream via regex rather than lowercasing 1MB payloads.
5. Optimized `ClassifyResponse` to run country code matching (`countryCodeRegex.FindSubmatch`) and location blocking (`locationBlockRegex.Match`) directly on `[]byte`, bypassing initial `string(body)` conversion.
6. Verified with comprehensive unit tests (`TestNormalizeText`, `TestNormalizeBytes`, and `TestClassifyResponse_Large1MBPayload_MaintainsPrecision`) preserving classification precision across all fixtures.
7. Benchmarks confirmed drastic memory allocation reductions on 1MB payloads:
   - `BenchmarkNormalizeText_1MB`: reduced from 8.14 MB (8 allocs) to 1.05 MB (1 alloc), an 87.1% memory reduction.
   - `BenchmarkClassifyResponse_1MB`: reduced from 10.26 MB (31 allocs) to 1.05 MB (9 allocs), an 89.8% memory reduction per probe.
