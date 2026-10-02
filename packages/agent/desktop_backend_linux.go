//go:build terva_desktop && terva_web && linux

package agent

import (
	"os"
	"terva.sh/terva/packages/i18n"

	"github.com/ebitengine/purego"
)

func prepareDesktopBackend() error {
	// Tuohi falls back even when TUOHI_BACKEND pins GTK4. Check the required
	// libraries before it can initialize GTK3 in this process.
	for _, library := range []string{"libgtk-4.so.1", "libwebkitgtk-6.0.so.4"} {
		handle, err := purego.Dlopen(library, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			return i18n.Errorf("terva desktop requires GTK4 and WebKitGTK 6.0: %w", err)
		}
		purego.Dlclose(handle)
	}
	return os.Setenv("TUOHI_BACKEND", "webkitgtk-6.0")
}

func checkDesktopBackend(backend string) error {
	if backend != "webkitgtk-6.0" {
		return i18n.Errorf("terva desktop requires WebKitGTK 6.0; native backend initialization failed")
	}
	return nil
}
