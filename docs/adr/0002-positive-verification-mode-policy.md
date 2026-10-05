# Positive-verification mode for Stage 2 classifiers

Stage 2 classifiers will support two verification modes — **strict
mode** (positive verification required) and **permissive mode** (HTTP
200 accepted at face value) — configurable per service target.

The default for built-in services (Gemini, Claude) is strict mode.
User-defined Tier 2 services default to permissive mode but can opt
into strict mode by providing positive-verification rules in their
config.

## Why this exists

Gemsub operates primarily in Iran, behind a national firewall that
deploys captive portals and transparent HTTP proxies. These
interception devices frequently return HTTP 200 with a valid HTML
page — but the page is the portal's login screen, a block notice, or
an injected redirect, not the actual target service response.

Without positive verification, a proxy that routes all traffic through
a captive portal would be classified as "passing" for every service.
The operator would receive a subscription list full of non-functional
proxies that all return HTTP 200 to *something*, but not to Gemini or
Claude.

Strict mode solves this by requiring the classifier to find
affirmative evidence that the response actually came from the target
service. For Gemini, this currently means: the response contains
WIZ_global_data, or Gemini-specific DOM markers, or the title
indicates a Gemini page. For Claude, this means: no redirect to
`app-unavailable-in-region`, no Cloudflare challenge page, and no
secondary region-block markers.

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
