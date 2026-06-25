package modes

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestBuildStudyPrompt(t *testing.T) {
	tmp := testsupport.TempDir(t)
	subdir := filepath.Join(tmp, "internal")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		arg  string
		cwd  string
		want string
	}{
		{
			name: "empty arg keeps the original cwd prompt",
			arg:  "",
			cwd:  tmp,
			want: "Read and understand everything in the current directory.",
		},
		{
			name: "relative dir becomes a directory prompt",
			arg:  "internal",
			cwd:  tmp,
			want: "Read and understand everything in the directory internal.",
		},
		{
			name: "absolute dir under cwd is shown as a relative path",
			arg:  subdir,
			cwd:  tmp,
			want: "Read and understand everything in the directory internal.",
		},
		{
			name: "relative file becomes a file prompt",
			arg:  "main.go",
			cwd:  tmp,
			want: "Read and understand the file main.go.",
		},
		{
			name: "absolute file under cwd is shown as a relative path",
			arg:  file,
			cwd:  tmp,
			want: "Read and understand the file main.go.",
		},
		{
			name: "missing path falls back to the directory phrasing",
			arg:  "does-not-exist",
			cwd:  tmp,
			want: "Read and understand everything in the directory does-not-exist.",
		},
		{
			name: "absolute path outside cwd keeps its absolute form",
			arg:  subdir,
			cwd:  filepath.Join(tmp, "elsewhere"),
			want: "Read and understand everything in the directory " + subdir + ".",
		},
		{
			name: "leading and trailing whitespace are stripped",
			arg:  "  main.go  ",
			cwd:  tmp,
			want: "Read and understand the file main.go.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildStudyPrompt(tc.arg, tc.cwd)
			if got != tc.want {
				t.Fatalf("buildStudyPrompt(%q, %q)\n  got:  %q\n  want: %q", tc.arg, tc.cwd, got, tc.want)
			}
		})
	}
}
