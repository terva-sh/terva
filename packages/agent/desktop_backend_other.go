//go:build terva_desktop && terva_web && !linux

package agent

import "terva.sh/terva/packages/i18n"

func prepareDesktopBackend() error { return nil }

func checkDesktopBackend(backend string) error {
	if backend == "" {
		return i18n.Errorf("terva desktop could not initialize the native backend; macOS requires WKWebView and Windows requires WebView2")
	}
	return nil
}
