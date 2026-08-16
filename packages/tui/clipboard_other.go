//go:build !darwin && !linux

package tui

import "fmt"

// ReadClipboardImagePNG is not implemented on this platform yet. Native
// Windows (GOOS=windows) is a deliberate TODO — when adding it, use the
// linux WSL branch (PowerShell + System.Windows.Forms.Clipboard) and the
// darwin osascript branch as templates. Returning an error gives the user a
// clear "not supported here" instead of a silent "no image".
func ReadClipboardImagePNG() ([]byte, bool, error) {
	return nil, false, fmt.Errorf("clipboard image paste is not supported on this platform yet")
}

// WriteClipboardText is not implemented on this platform yet, for the reason
// above. An explicit refusal keeps a caller from reporting "copied" about a
// clipboard that received nothing.
func WriteClipboardText(string) error {
	return fmt.Errorf("copying to the clipboard is not supported on this platform yet")
}
