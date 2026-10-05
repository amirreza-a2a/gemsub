# Config schema for Stage 2 service targets with legacy hydration

Service targets will be configured under `test.services.<name>` in
the JSON config file. The existing `test.gemini`, `test.claude`,
`test.target_url`, and `test.block_phrases` fields will continue to
work during migration via a legacy hydration path in `config.Validate()`.

## Schema

```jsonc
{
  "test": {
    // Stage 2 can be disabled entirely for transport-only operation:
    "skip_stage2": false,

    // New canonical service configuration:
    "services": {
      "gemini": {
        "url": "https://gemini.google.com/",
        "timeout": "10s",
        "dial_timeout": "4s",
        "mode": "strict",
        "rules": []  // empty = use built-in default rule set
      },
      "claude": {
        "enabled": true,
        "url": "https://claude.ai/",
        "timeout": "10s",
        "dial_timeout": "5s",
        "mode": "strict",
        "rules": []  // empty = use built-in default rule set
      },
      "my-custom-api": {
        "url": "https://api.example.com/health",
        "timeout": "5s",
        "mode": "permissive",
        "rules": [
          {
            "signal": "http_status",
            "match": { "equals": 200 },
            "verdict": { "status": "passed" }
          }
        ]
      }
    },

    // Legacy fields (still parsed, hydrated into test.services):
    "target_url": "https://gemini.google.com/",
    "block_phrases": ["..."],
    "gemini": { "url": "...", "block_phrases": ["..."] },
    "claude": { "enabled": true, "url": "...", "timeout": "..." }
  }
}
```

## Legacy hydration

During the migration period (phases 3–5 of ADR-0004), `config.Validate()`
will synthesize `test.services` entries from legacy fields when the
new `test.services` map is absent or incomplete:

1. If `test.services.gemini` is absent, construct it from
   `test.gemini.url` / `test.gemini.block_phrases`, falling back to
   `test.target_url` / `test.block_phrases` if `test.gemini` is also
   absent.
2. If `test.services.claude` is absent, construct it from
   `test.claude.*`.
3. Legacy fields take lower precedence: if both `test.services.gemini`
   and `test.gemini` are present, `test.services.gemini` wins.
4. `test.skip_stage2` is new; it defaults to `false`.

This continues the existing hydration pattern already established in
`config.go` lines 148–164 (where `test.gemini.url` falls back to
`test.target_url` and `test.gemini.block_phrases` falls back to
`test.block_phrases`).

## Consequences

- **No breaking change for existing configs.** The current
  `config.example.json` continues to work without modification.
  Users see no difference until they choose to adopt the new
  `test.services` syntax.
- **Legacy fields will be deprecated in phase 6** (ADR-0004) but
  never removed from parsing — only from documentation and default
  templates. Old config files will continue to load.
- **`test.skip_stage2: true`** skips all Stage 2 probing, producing
  only transport-health projections. This is useful for operators who
  want a generic proxy-health tool without service-specific
  classification.
- User-defined Tier 2 services (e.g. `my-custom-api` above) are
  configured entirely in this map. They get the live-only Store
  treatment described in ADR-0003.
