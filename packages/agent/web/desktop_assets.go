//go:build terva_desktop && terva_web

package web

// DesktopIcon returns the panel's PNG icon for the native application.
func DesktopIcon() []byte {
	icon, _ := clientFS.ReadFile("client/dist/pwa-512.png")
	return icon
}
