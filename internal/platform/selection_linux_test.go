//go:build linux && cgo && !gtk3

package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrimarySelectionStreamLimitsAndCancellation(t *testing.T) {
	// Test the C implementation against GIO memory streams. This never opens a
	// display, changes focus, or reads either of the user's clipboards.
	source, err := os.ReadFile("selection_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	_, preamble, ok := strings.Cut(string(source), "/*\n")
	if !ok {
		t.Fatal("missing cgo preamble")
	}
	preamble, _, ok = strings.Cut(preamble, "*/")
	if !ok {
		t.Fatal("unterminated cgo preamble")
	}
	var lines []string
	for _, line := range strings.Split(preamble, "\n") {
		if !strings.HasPrefix(line, "#cgo ") {
			lines = append(lines, line)
		}
	}
	fixture, err := os.ReadFile("testdata/selection_stream.c")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selection-stream.c")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+string(fixture)), 0600); err != nil {
		t.Fatal(err)
	}
	flags, err := exec.Command("pkg-config", "--cflags", "--libs", "gtk4").Output()
	if err != nil {
		t.Fatal(err)
	}
	binary := strings.TrimSuffix(path, ".c")
	args := append([]string{path, "-o", binary}, strings.Fields(string(flags))...)
	if out, err := exec.Command("cc", args...).CombinedOutput(); err != nil {
		t.Fatalf("compile stream fixture: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
		t.Fatalf("native selection transfer: %v\n%s", err, out)
	}
}
