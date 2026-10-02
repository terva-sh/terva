package testsupport

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDesktopLoader(t *testing.T) {
	dir := TempDir(t)
	checker := filepath.Join(dir, "check-loader")
	if runtime.GOOS == "windows" {
		checker += ".exe"
	}
	source := filepath.Join(repoRoot, "scripts", "check-desktop-loader.go")
	if _, err := os.ReadFile(source); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", checker, source)
	build.Env = append(os.Environ(), "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build checker: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, goos, arch, loader, want string
		machine                        elf.Machine
		ok                             bool
	}{
		{"amd64", "linux", "amd64", "/lib64/ld-linux-x86-64.so.2", "uses /lib64/ld-linux-x86-64.so.2", elf.EM_X86_64, true},
		{"arm64", "linux", "arm64", "/lib/ld-linux-aarch64.so.1", "uses /lib/ld-linux-aarch64.so.1", elf.EM_AARCH64, true},
		{"musl", "linux", "amd64", "/lib/ld-musl-x86_64.so.1", "must be", elf.EM_X86_64, false},
		{"wrong loader", "linux", "arm64", "/lib64/ld-linux-x86-64.so.2", "must be", elf.EM_AARCH64, false},
		{"wrong machine", "linux", "amd64", "/lib64/ld-linux-x86-64.so.2", "does not match", elf.EM_AARCH64, false},
		{"static", "linux", "amd64", "", "0 interpreters", elf.EM_X86_64, false},
		{"unsupported", "linux", "386", "", "unsupported", elf.EM_386, false},
		{"darwin", "darwin", "arm64", "", "does not use", elf.EM_AARCH64, true},
		{"windows", "windows", "amd64", "", "does not use", elf.EM_X86_64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".elf")
			writeDesktopELF(t, path, tc.machine, tc.loader)
			out, err := exec.Command(checker, tc.goos, tc.arch, path).CombinedOutput()
			if (err == nil) != tc.ok || !strings.Contains(string(out), tc.want) {
				t.Fatalf("checker: %v\n%s", err, out)
			}
		})
	}
	if out, err := exec.Command(checker, "linux", "amd64", filepath.Join(dir, "absent")).CombinedOutput(); err == nil || !strings.Contains(string(out), "open desktop binary") {
		t.Fatalf("missing binary: %v\n%s", err, out)
	}
}

func writeDesktopELF(t *testing.T, path string, machine elf.Machine, loader string) {
	t.Helper()
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1})
	binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(data[18:], uint16(machine))
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint16(data[52:], 64)
	if loader != "" {
		binary.LittleEndian.PutUint64(data[32:], 64)
		binary.LittleEndian.PutUint16(data[54:], 56)
		binary.LittleEndian.PutUint16(data[56:], 1)
		prog := make([]byte, 56)
		binary.LittleEndian.PutUint32(prog, uint32(elf.PT_INTERP))
		binary.LittleEndian.PutUint64(prog[8:], 120)
		binary.LittleEndian.PutUint64(prog[32:], uint64(len(loader)+1))
		data = append(data, prog...)
		data = append(data, loader...)
		data = append(data, 0)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
