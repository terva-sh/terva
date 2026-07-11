package modes

import "testing"

// ctrl+t cycles the transcript's tool display: boxes → minimal →
// grouped → hidden → boxes, announcing each state on the status line.
func TestCtrlTCyclesToolDisplay(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("\x14") // ctrl+t
	h.waitText("tool display: minimal")
	h.term.Type("\x14")
	h.waitText("tool display: grouped")
	h.term.Type("\x14")
	h.waitText("tool display: hidden")
	h.term.Type("\x14")
	h.waitText("tool display: boxes")
}
