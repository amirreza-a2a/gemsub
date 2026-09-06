# 06: perf(classifier): optimize NormalizeText memory allocations

Type: task
Status: open
Blocked by: None (can start immediately)

## Question

How do we eliminate massive heap allocations when classifying candidate responses on 1MB payloads?

## What to build

Refactor text normalization in `internal/tester/classifier.go` to avoid `strings.Fields` word-token array allocations across 1MB HTML bodies, bounding text inspection to relevant document segments and using streaming/byte-level scanning.

## Acceptance criteria

- [ ] `NormalizeText` allocations reduced significantly on large HTML bodies.
- [ ] Classification precision maintained across all existing classifier test fixtures.
- [ ] Benchmark verifies reduction in allocated bytes per probe.
