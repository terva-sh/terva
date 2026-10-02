package build

import (
	"fmt"
	"terva.sh/terva/packages/i18n"
)

// PrintDesktopHelp prints help for the owned desktop window.
func PrintDesktopHelp() {
	fmt.Println(i18n.T(`Usage: terva desktop [options]

Open one native window with a new local server. Closing the window stops
its server and preserves sessions. A live daemon in the same TERVA_HOME
prevents startup. Use a separate TERVA_HOME to run both.

  --desktop-port PORT    use this loopback port (default: random free port)
  --web-allow-login      allow provider login from the Providers pane
  --cwd DIR              use this workspace directory

Install the separate desktop artifact. Source builds need both
terva_desktop and terva_web tags. Linux requires glibc, GTK4 and
WebKitGTK 6.0. macOS uses WKWebView. Windows requires WebView2.
Daemon attachment will come later.`))
}
