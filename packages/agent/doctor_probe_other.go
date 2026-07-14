//go:build !linux

package agent

// Off Linux these facts either do not exist (no_new_privs is a Linux prctl) or
// are not checked in this first slice. They report "unknown" so the doctor
// stays honest rather than implying a posture it never verified. See
// doctor_probe_linux.go for the real probes.

func noNewPrivs() string { return "unknown (Linux-only)" }

func coreDumpStatus() string { return "unknown (not checked on this platform)" }
