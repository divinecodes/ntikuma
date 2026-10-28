//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func fileRegistry(t *testing.T, roots ...string) *Registry {
	t.Helper()
	registry := new(Registry)
	for _, tool := range FileTools(roots) {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func runPath(registry *Registry, name, path string) string {
	args, _ := json.Marshal(map[string]string{"path": path})
	return registry.Run(context.Background(), toolCall(name, string(args)))
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestFileToolsReadTextListFoldersAndSnapshotRoots(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "todo.md")
	writeFile(t, file, []byte("# Tasks\nKeep Unicode: Accra 🙂\n"))
	if err := os.Mkdir(filepath.Join(root, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("todo.md", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "line\nbreak"), []byte("name test"))
	roots := []string{root}
	registry := fileRegistry(t, roots...)
	roots[0] = "/not/the/original/root"
	if output := runPath(registry, "read_file", file); output != "# Tasks\nKeep Unicode: Accra 🙂\n" {
		t.Fatal(output)
	}
	if output := runPath(registry, "read_file", filepath.Join(root, "alias")); !strings.Contains(output, "Accra") {
		t.Fatal(output)
	}
	listing := runPath(registry, "list_directory", root)
	for _, expected := range []string{`directory` + "\t" + `"notes"`, `file` + "\t" + `"todo.md"`, `symlink` + "\t" + `"alias"`, `"line\nbreak"`} {
		if !strings.Contains(listing, expected) {
			t.Fatalf("missing %s: %s", expected, listing)
		}
	}
	if strings.Index(listing, `"notes"`) > strings.Index(listing, `"todo.md"`) {
		t.Fatal("entries not sorted")
	}
}

func TestFileToolsRefuseEscapesBinaryMissingAndUnexpectedArguments(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "allowed-other")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "secret.txt")
	writeFile(t, secret, []byte("MUST NEVER RETURN THIS"))
	writeFile(t, filepath.Join(root, "binary"), []byte{0xff, 0xfe, 0x00})
	writeFile(t, filepath.Join(root, "nul"), []byte{'a', 0, 'b'})
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	registry := fileRegistry(t, root)
	for _, path := range []string{"../../etc/passwd", "/etc/passwd", root + "/../../etc/passwd", secret, filepath.Join(root, "escape/secret.txt"), filepath.Join(root, "missing"), filepath.Join(root, "binary"), filepath.Join(root, "nul"), root} {
		output := runPath(registry, "read_file", path)
		if !strings.HasPrefix(output, "error: ") || strings.Contains(output, "MUST NEVER RETURN THIS") {
			t.Fatalf("unsafe read %q: %q", path, output)
		}
	}
	for _, args := range []string{`{}`, `{"path":1}`, `{"path":"` + root + `","extra":true}`} {
		if output := registry.Run(context.Background(), toolCall("read_file", args)); !strings.HasPrefix(output, "error:") {
			t.Fatal(output)
		}
	}
	if output := runPath(fileRegistry(t), "read_file", secret); !strings.Contains(output, "outside") {
		t.Fatal("empty roots allowed access", output)
	}
}

func TestFileToolsHonorHomeRootsAliasesAndMissingInactiveFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docs := filepath.Join(home, "Documents")
	if err := os.Mkdir(docs, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(docs, "note"), []byte("authorized"))
	if err := os.Symlink(docs, filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, roots := range [][]string{{"~/missing", "~/Documents"}, {"~/alias"}} {
		registry := fileRegistry(t, roots...)
		if output := runPath(registry, "read_file", "~/Documents/note"); output != "authorized" {
			t.Fatal(output)
		}
	}
}

func TestFileToolsBoundFilesAndDirectoryListings(t *testing.T) {
	root := t.TempDir()
	registry := fileRegistry(t, root)
	file := filepath.Join(root, "large")
	writeFile(t, file, []byte(strings.Repeat("x", MaxFileBytes)))
	if output := runPath(registry, "read_file", file); strings.HasPrefix(output, "error:") || utf8.RuneCountInString(output) != MaxOutputChars || !strings.HasSuffix(output, "[truncated]") {
		t.Fatal("1 MiB boundary failed")
	}
	if err := os.Truncate(file, MaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if output := runPath(registry, "read_file", file); !strings.Contains(output, "1 MiB") {
		t.Fatal(output)
	}
	for i := range MaxDirectoryEntries + 10 {
		writeFile(t, filepath.Join(root, fmt.Sprintf("file-%03d", i)), []byte("small"))
	}
	listing := runPath(registry, "list_directory", root)
	if !strings.Contains(listing, "listing truncated after 256 entries") || len(strings.Split(strings.TrimSpace(listing), "\n")) != MaxDirectoryEntries+2 {
		t.Fatal("unbounded directory listing")
	}
	if output := runPath(registry, "list_directory", file); !strings.Contains(output, "not a directory") {
		t.Fatal(output)
	}
}

func TestFileToolsRefuseSpecialFilesWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	registry := fileRegistry(t, root)
	done := make(chan string, 1)
	go func() { done <- runPath(registry, "read_file", fifo) }()
	select {
	case result := <-done:
		if !strings.Contains(result, "not a regular file") {
			t.Fatal(result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening a FIFO blocked before rejecting it")
	}
}

func TestConcurrentDirectorySymlinkReplacementNeverEscapes(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "allowed"), filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	directory, parked := filepath.Join(root, "mutable"), filepath.Join(root, "parked")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, "data"), []byte("INSIDE"))
	writeFile(t, filepath.Join(outside, "data"), []byte("OUTSIDE SECRET"))
	registry := fileRegistry(t, root)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if os.Rename(directory, parked) != nil {
				continue
			}
			_ = os.Symlink(outside, directory)
			_ = os.Remove(directory)
			_ = os.Rename(parked, directory)
		}
	})
	defer func() { close(stop); wg.Wait() }()
	for range 1000 {
		output := runPath(registry, "read_file", filepath.Join(directory, "data"))
		if output != "INSIDE" && !strings.HasPrefix(output, "error:") {
			t.Fatalf("root confinement failed: %q", output)
		}
	}
}
