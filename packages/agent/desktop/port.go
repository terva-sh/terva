package desktop

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"

	"terva.sh/terva/packages/privfs"
)

// StateFile is the name of the desktop window's state file in TERVA_HOME. It
// lives in the home, so each home remembers its own port and two homes never
// share one through it.
const StateFile = "desktop.json"

// state is what the desktop window remembers between launches.
type state struct {
	Port      int    `json:"port,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// readState reads the state file. A field that fails its check reads as
// absent, and a file that does not parse reads as empty.
func readState(path string) state {
	var s state
	data, err := os.ReadFile(path)
	if err != nil {
		return state{}
	}
	if json.Unmarshal(data, &s) != nil {
		return state{}
	}
	if s.Port < 1 || s.Port > 65535 {
		s.Port = 0
	}
	return s
}

func writeState(path string, s state) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return privfs.WriteFile(path, data)
}

// PickPort chooses the loopback port for a launch.
//
// 🔑 The webview keeps localStorage per origin, and the port is part of the
// origin. A launch on the port the last one bound therefore opens with the
// app's saved preferences: the person's name, the open team, the face motion.
//
// An explicit port always wins. Otherwise the remembered port is reused when
// free reports it free. When it is busy, PickPort returns 0, a random port,
// and busy names the remembered port, so the caller can say that this launch
// starts with fresh preferences.
func PickPort(path string, explicit int, free func(int) bool) (port, busy int) {
	if explicit > 0 {
		return explicit, 0
	}
	last := readState(path).Port
	if last == 0 {
		return 0, 0
	}
	if free(last) {
		return last, 0
	}
	return 0, last
}

// RememberPort records the port a launch bound, for the next launch.
func RememberPort(path string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("desktop: no port to remember")
	}
	s := readState(path)
	s.Port = port
	return writeState(path, s)
}

// PortFree reports whether 127.0.0.1:port accepts a listener now.
//
// ⚠️ The probe closes its listener before the server binds, so another process
// can take the port in between. The server's bind then fails and the launch
// stops with that error. A second launch picks a random port, because this one
// never records the lost port.
func PortFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	return l.Close() == nil
}
