//go:build darwin

package tui

import "testing"

func TestClipboardImageKindIgnoresLeadingNoise(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   string
	}{
		{"plain png", "png\n", "png"},
		{"plain tiff", "tiff", "tiff"},
		{"no image", "NO_IMAGE\n", "NO_IMAGE"},
		{"empty", "\n  \n", ""},
		{
			// The reported failure: ImageIO logged a JP2 color-space
			// diagnostic while osascript still exited 0.
			"imageio diagnostic",
			"*** Error creating a JP2 color space: falling back to sRGB\npng\n",
			"png",
		},
		{
			"several diagnostics",
			"*** one\n*** two\ntiff\n",
			"tiff",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clipboardImageKind(tc.stdout); got != tc.want {
				t.Fatalf("clipboardImageKind(%q) = %q, want %q", tc.stdout, got, tc.want)
			}
		})
	}
}

func TestClipboardNoImage(t *testing.T) {
	yes := []string{
		"NO_IMAGE",
		"execution error: Can’t make «class PNGf» into type string.",
		"execution error: Can't make some data into the expected type.",
	}
	for _, msg := range yes {
		if !clipboardNoImage(msg) {
			t.Fatalf("clipboardNoImage(%q) = false, want true", msg)
		}
	}
	no := []string{
		"",
		"*** Error creating a JP2 color space: falling back to sRGB",
		"execution error: File permission error.",
	}
	for _, msg := range no {
		if clipboardNoImage(msg) {
			t.Fatalf("clipboardNoImage(%q) = true, want false", msg)
		}
	}
}
