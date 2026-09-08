# 17: feat(config): separate transport and gemini probe configuration

Type: feature
Status: resolved
Blocked by: None (can start immediately)

## Problem

The current `config.TestConfig` defines a single ambiguous `target_url` field (defaulting to `"https://gemini.google.com/"`) and a flat `block_phrases` array. This couples the generic transport network endpoint with Gemini-specific application configuration. There is currently no way to configure an independent transport health check endpoint (`health_url`) or its timeout (`health_timeout`), nor is there any namespace separating Gemini's target URL and block phrases from transport configuration.

## Scope

1. Introduce `GeminiConfig` struct containing:
   - `URL string` (`json:"url"`)
   - `BlockPhrases []string` (`json:"block_phrases"`)
2. Augment `TestConfig` with:
   - `HealthURL string` (`json:"health_url"`)
   - `HealthTimeoutRaw string` (`json:"health_timeout,omitempty"`)
   - `HealthTimeout time.Duration` (`json:"-"`)
   - `Gemini GeminiConfig` (`json:"gemini"`)
   - Retain deprecated `TargetURL string` (`json:"target_url,omitempty"`)
   - Retain deprecated `BlockPhrases []string` (`json:"block_phrases,omitempty"`)
3. Implement backward-compatible normalization in `Validate()`:
   - If `Gemini.URL` is unset and legacy `TargetURL` is provided, assign `Gemini.URL = TargetURL`.
   - If `Gemini.BlockPhrases` is empty and legacy `BlockPhrases` is provided, assign `Gemini.BlockPhrases = BlockPhrases`.
   - Default `HealthURL` to `"https://www.gstatic.com/generate_204"`.
   - Default `Gemini.URL` to `"https://gemini.google.com/"`.
   - Default `HealthTimeout` to `4s` (or `DialTimeout` if set).
   - Ensure `Gemini.BlockPhrases` is non-empty after legacy mapping.
4. Update `config.example.json` to showcase the new namespaced configuration layout.

## Acceptance Criteria

- [x] `config.TestConfig` cleanly exposes `HealthURL`, `HealthTimeout`, and `Gemini` (`URL`, `BlockPhrases`).
- [x] Existing configurations omitting `health_url` default safely to `"https://www.gstatic.com/generate_204"`.
- [x] Existing configurations omitting `health_timeout` default safely to `4s`.
- [x] Legacy configurations using top-level `target_url` and `block_phrases` load without errors and populate `Gemini.URL` and `Gemini.BlockPhrases`.
- [x] Explicit new configurations using `test.gemini.url` and `test.gemini.block_phrases` load and validate without requiring deprecated fields.
- [x] Invalid duration strings for `health_timeout` fail validation with clear error messages.
- [x] Unit tests in `internal/config/config_test.go` cover default assignment, legacy fallback compatibility, new syntax parsing, and validation rejection.

## Implementation Summary

1. **Config Structs**:
   - Added `GeminiConfig` struct (`URL`, `BlockPhrases`).
   - Extended `TestConfig` with `HealthURL`, `HealthTimeoutRaw`, `HealthTimeout`, and `Gemini`.
   - Preserved `TargetURL` and `BlockPhrases` with `json:",omitempty"` for transparent backward compatibility.

2. **Validation & Normalization**:
   - `Gemini.URL`: if empty, falls back to legacy `TargetURL` or defaults to `"https://gemini.google.com/"`. Synced back to `TargetURL` so legacy callers work without change.
   - `Gemini.BlockPhrases`: if empty, falls back to legacy `BlockPhrases`. Validates non-empty and syncs back to `BlockPhrases`.
   - `HealthURL`: defaults to `"https://www.gstatic.com/generate_204"`.
   - `HealthTimeout`: parsed from `HealthTimeoutRaw` if provided; defaults to `DialTimeout` (or `4s` if unset).

3. **Example Config & Tests**:
   - Updated `config.example.json` with namespaced canonical structure.
   - Added 10 new unit tests in `internal/config/config_test.go` covering defaults, inheritance, explicit overrides, invalid durations, legacy fallbacks, new syntax, precedence, missing phrases, `config.example.json`, and existing `config.json`.

## Explicit Non-Goals

- Do not introduce generic target arrays (e.g. `targets: []`).
- Do not add Claude configuration fields in this ticket.
- Do not modify tester, store, or scheduler runtime probe code.

## Architectural Invariants Preserved

- `Transport Health != Gemini Compatibility`: Configuration parameters for transport health (`health_url`, `health_timeout`) are strictly separated from Gemini application parameters (`gemini.url`, `gemini.block_phrases`).
- `Backward Compatibility`: Existing `config.json` files continue to function identically without requiring manual editing.

## Expected Files/Modules

- `internal/config/config.go`
- `internal/config/config_test.go`
- `config.example.json`

## Test Requirements

- Test default population when all new fields are omitted.
- Test legacy fallback when only `target_url` and `block_phrases` are supplied.
- Test precedence when both legacy and new fields are present (new fields take priority).
- Test parsing of custom `health_url` and `health_timeout`.
- Test validation failures when block phrases are completely missing.
