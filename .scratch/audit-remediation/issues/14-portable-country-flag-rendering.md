# 14: feat(tui): portable country flag rendering

Type: feature
Status: resolved
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

- [x] Country display remains readable on terminals without emoji/color-emoji rendering support.
- [x] Preferred Unicode rendering is supported (e.g. 🇩🇪 Germany).
- [x] Portable fallback rendering is supported (e.g. [DE] Germany).
- [x] Implementation does not rely on Nerd Font availability or assume fc-match guarantees terminal emoji support.
- [x] Explicit presentation abstraction provided (auto | unicode | ascii) with auto as default.
- [x] Country/flag rendering decisions do not affect candidate identity or Store state.
- [x] Focused tests for both Unicode and fallback representations added.
- [x] Frozen architecture document is preserved.

## Answer

Implemented portable country and flag presentation in `internal/tui/country` and wired into `internal/tui/adapter`, `internal/config`, and `cmd/gemsub`:

1. **Root Cause**:
   - Regional indicator Unicode sequences (e.g. `🇩🇪`) require terminal emulator support for Unicode regional indicator sequence ligature composition.
   - Font availability (such as `Noto Color Emoji`) or `COLORTERM=truecolor` on the host does NOT guarantee that the terminal emulator (or client over SSH) supports emoji flags.
   - On minimal Linux environments (`TERM=linux`, `TERM=dumb`), SSH sessions, or terminals without dedicated emoji shaping, regional indicator flags render as broken missing-glyph boxes (`[?] [?]`) or cause character cell width misalignments.

2. **Chosen Presentation Abstraction**:
   - `country.Mode`: `auto`, `unicode`, `ascii`, with `auto` as the default.
   - `country.FormatCountry(code, name, mode)`:
     - `ModeUnicode`: `"🇩🇪 Germany"`
     - `ModeASCII`: `"[DE] Germany"`
   - `country.FormatRemark(remark, mode)`:
     - In `ModeASCII`: Converts any 2-rune Unicode regional indicator flag emojis into portable bracketed ASCII codes (`"[DE] Germany 01"`).
     - In `ModeUnicode`: Preserves existing Unicode emoji flags and converts recognizable bracketed country codes into Unicode flag emojis (`"🇩🇪 Germany"`).
   - Conservative `ModeAuto` heuristic:
     - Checks explicit environment override `GEMSUB_FLAG_MODE` (`unicode` or `ascii`).
     - Detects SSH sessions (`SSH_CLIENT`, `SSH_TTY`, `SSH_CONNECTION`) and minimal/dumb terminals (`TERM=linux`, `TERM=dumb`, `LANG` non-UTF-8), conservatively resolving to `ModeASCII`.
     - Deliberately does NOT assume generic `TERM=xterm-256color` or `fc-match` guarantees flag rendering.
     - Resolves to `ModeUnicode` only when a known emoji-capable modern terminal is detected (`iTerm.app`, `Apple_Terminal`, `wezterm`, `ghostty`, `WT_SESSION`, Kitty) with UTF-8 locale and no SSH session.
     - Falls back to `ModeASCII` otherwise to guarantee universal readability.

3. **Presentation-Layer Boundary**:
   - All formatting occurs strictly during ViewModel assembly in `Adapter.CandidateRows` and `Adapter.CandidateDetail`.
   - Candidate identity, `Store.Store`, `CandidateRecord`, `CanonicalLink`, `ActiveLink`, scoring, and parsing remain completely untouched.
   - Clipboard link copying retains raw, unmasked, unmodified links.
   - `docs/architecture/tui-mvp-design.md` preserved without modification.

4. **Verification**:
   - `internal/tui/country/country_test.go`: Complete coverage for Unicode mode, ASCII fallback mode, Auto resolution matrix (SSH, minimal Linux, non-UTF8, modern terminals, env overrides), representative country codes, invalid/empty code inputs, and remark conversions.
   - `internal/tui/adapter/adapter_test.go`: `TestAdapter_FlagPresentationModes` verifying ASCII vs Unicode remark presentation and store record immutability.
   - `internal/tui/model_test.go`: `TestModel_CountryFlagRendering` verifying table and inspection detail rendering.
   - `internal/config/config_test.go` and `cmd/gemsub/main_test.go`: Config validation and CLI flag parsing.
