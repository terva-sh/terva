package extdriver

import "terva.sh/terva/packages/agent/extproto"

// stubHooks is a no-op HostHooks for driver-level tests that need a
// Driver but don't exercise the host callback surface.
type stubHooks struct{}

func (stubHooks) Notify(extName, level, message string)             {}
func (stubHooks) Submit(text string)                                {}
func (stubHooks) SubmitSlash(text string)                           {}
func (stubHooks) Insert(text string)                                {}
func (stubHooks) Display(extName, text string)                      {}
func (stubHooks) ClearNotes(extName string)                         {}
func (stubHooks) OpenPanel(extName string, spec extproto.PanelSpec) {}
func (stubHooks) UpdatePanel(a, b, c string, d []string, e string)  {}
func (stubHooks) ClosePanel(extName, panelID string)                {}
func (stubHooks) RefreshStatus()                                    {}
func (stubHooks) RefreshContext()                                   {}
