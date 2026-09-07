# 14: feat(tui): portable country flag rendering

Type: feature
Status: ready-for-agent
Blocked by: None (can start immediately)

## Problem

Country flags are rendered using emoji such as 🇩🇪, 🇫🇷, 🇺🇸. Terminal rendering is not guaranteed even when Noto Color Emoji is installed.

Current environment evidence:
TERM=xterm-256color
COLORTERM=truecolor
fc-match "Noto Color Emoji" resolves to Noto Color Emoji.

## Required contract

- Country display must remain readable on terminals without emoji/color-emoji rendering support.
- Preferred Unicode rendering:
  🇩🇪 Germany
- Portable fallback:
  [DE] Germany
- The implementation must not rely on Nerd Font availability.
- Do not assume fc-match proves terminal emoji support.
- Prefer an explicit presentation abstraction such as:
  auto | unicode | ascii
  with auto as the default, unless the existing architecture suggests a better equivalent.
- No country/flag rendering decision should affect candidate identity or Store state.
- Add focused tests for both Unicode and fallback representations where practical.
- Do not modify the frozen architecture document unless strictly necessary.

## Acceptance criteria

- [ ] Country display remains readable on terminals without emoji/color-emoji rendering support.
- [ ] Preferred Unicode rendering is supported (e.g. 🇩🇪 Germany).
- [ ] Portable fallback rendering is supported (e.g. [DE] Germany).
- [ ] Implementation does not rely on Nerd Font availability or assume fc-match guarantees terminal emoji support.
- [ ] Explicit presentation abstraction provided (auto | unicode | ascii) with auto as default.
- [ ] Country/flag rendering decisions do not affect candidate identity or Store state.
- [ ] Focused tests for both Unicode and fallback representations added.
- [ ] Frozen architecture document is preserved.
