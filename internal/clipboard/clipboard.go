package clipboard

import (
	"fmt"
	"os/exec"
)

// CopyToClipboard attempts to copy text to the system clipboard.
// It tries native clipboard tools (wl-copy, xclip, xsel, pbcopy).
// If clipboard capability is unavailable, it fails non-fatally and returns an error.
func CopyToClipboard(text string) error {
	if text == "" {
		return fmt.Errorf("empty text")
	}

	// Try wl-copy (Wayland)
	if path, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.Command(path)
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				_, _ = stdin.Write([]byte(text))
				_ = stdin.Close()
				if cmd.Wait() == nil {
					return nil
				}
			}
		}
	}

	// Try xclip (X11)
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command(path, "-selection", "clipboard")
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				_, _ = stdin.Write([]byte(text))
				_ = stdin.Close()
				if cmd.Wait() == nil {
					return nil
				}
			}
		}
	}

	// Try xsel (X11)
	if path, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command(path, "--clipboard", "--input")
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				_, _ = stdin.Write([]byte(text))
				_ = stdin.Close()
				if cmd.Wait() == nil {
					return nil
				}
			}
		}
	}

	// Try pbcopy (macOS)
	if path, err := exec.LookPath("pbcopy"); err == nil {
		cmd := exec.Command(path)
		stdin, err := cmd.StdinPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				_, _ = stdin.Write([]byte(text))
				_ = stdin.Close()
				if cmd.Wait() == nil {
					return nil
				}
			}
		}
	}

	return fmt.Errorf("no supported clipboard tool available (requires wl-copy, xclip, xsel, or pbcopy)")
}
