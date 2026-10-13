# Positive-verification mode for Stage 2 classifiers

Stage 2 classifiers will support two verification modes — **strict
mode** (positive verification required) and **permissive mode** (HTTP
200 accepted at face value) — configurable per service target.

The default for user-defined Tier 2 services is **strict mode**;
`config.Validate()` fails fast if a service configuration lacks
positive-verification rules unless `mode: "permissive"` is explicitly
declared.
For built-in Tier 1 services, Gemini operates in **strict mode**
(requiring positive proof), while Claude operates in **permissive mode**
(accepting HTTP 200 after all negative and interception checks pass).

## Fallback semantics

The rule engine executes fallback verdicts when no explicit rule matches:

1. **Strict mode fallback**: If all negative rejection and interception
   checks pass but no positive-verification rule matches, the verdict is:
   `Status: store.StatusFailed, Category: store.ErrTargetOther, Reason: "possible interception / false positive: positive verification signals absent"`.
   A bare HTTP 200 from an unknown or captive-portal response never passes.
2. **Permissive mode fallback**: If all negative rejection and interception
   checks pass and the HTTP status is within `accepted_status_codes` (default
   `[200]`), the verdict is:
   `Status: store.StatusPassed, Category: store.ErrNone, Reason: "ok"`.

## Why this exists

Gemsub operates primarily in Iran, behind a national firewall that
deploys captive portals and transparent HTTP proxies. These
interception devices frequently return HTTP 200 with a valid HTML
page — but the page is the portal's login screen, a block notice, or
an injected redirect, not the actual target service response.

Without positive verification, a proxy that routes all traffic through
a captive portal would be classified as "passing" for every service.
The operator would receive a subscription list full of non-functional
proxies that all return HTTP 200 to *something*, but not to Gemini.

Strict mode solves this by requiring the classifier to find
affirmative evidence that the response actually came from the target
service. For Gemini, this currently means: the response contains
WIZ_global_data, or Gemini-specific DOM markers, or the title
indicates a Gemini page. For Claude, which operates in permissive
mode, negative checks verify no redirect to `app-unavailable-in-region`,
no Cloudflare challenge page, and no secondary region-block markers
before accepting HTTP 200.

## Consequences

- **Strict mode increases false negatives** (a real Gemini response
  that happens to lack the expected markers would be rejected), but
  this is strongly preferred over false positives in the operating
  environment. A false negative means one good proxy is excluded from
  the subscription. A false positive means an end user's traffic goes
  through a captive portal.

- **Permissive mode is not "less secure"** — it is appropriate for
  services where the operator controls the network path or where
  captive-portal interception is not a realistic threat.

- **The mode is a property of the service's rule set**, not a global
  toggle. A single gemsub instance can run Gemini in strict mode
  and a user-defined service in permissive mode simultaneously.

- **Future readers**: if you are considering removing positive
  verification or defaulting to permissive mode, re-read this ADR.
  The decision exists because of observed real-world captive-portal
  behavior on Iranian networks, not as an arbitrary strictness
  choice.
