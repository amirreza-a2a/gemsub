package tui

import (
	"gemsub/internal/clipboard"
)

// CopyToClipboard attempts to copy text to the system clipboard.
// If clipboard capability is unavailable, it fails non-fatally and returns an error.
func CopyToClipboard(text string) error {
	return clipboard.CopyToClipboard(text)
}
