//go:build ignore

package main

import (
	"debug/elf"
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: check-desktop-loader GOOS GOARCH BINARY")
		os.Exit(1)
	}
	if err := check(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check(goos, arch, path string) error {
	if goos != "linux" {
		fmt.Printf("desktop loader: %s/%s does not use the Linux ELF loader\n", goos, arch)
		return nil
	}
	var machine elf.Machine
	var loader string
	switch arch {
	case "amd64":
		machine, loader = elf.EM_X86_64, "/lib64/ld-linux-x86-64.so.2"
	case "arm64":
		machine, loader = elf.EM_AARCH64, "/lib/ld-linux-aarch64.so.1"
	default:
		return fmt.Errorf("unsupported Linux desktop architecture %q", arch)
	}
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("open desktop binary: %w", err)
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != machine {
		return fmt.Errorf("desktop binary does not match linux/%s: %s %s %s", arch, f.Class, f.Data, f.Machine)
	}
	count := 0
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		count++
		data, err := io.ReadAll(io.LimitReader(p.Open(), int64(len(loader)+2)))
		if err != nil {
			return fmt.Errorf("read desktop interpreter: %w", err)
		}
		if string(data) != loader+"\x00" || p.Filesz != uint64(len(loader)+1) {
			return fmt.Errorf("desktop interpreter %q must be %q", data, loader)
		}
	}
	if count != 1 {
		return fmt.Errorf("desktop binary has %d interpreters; expected one", count)
	}
	fmt.Printf("desktop loader: linux/%s uses %s\n", arch, loader)
	return nil
}
