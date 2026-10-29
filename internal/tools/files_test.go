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

func TestFileToolsRefuseHiddenAndKeyFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.md"), []byte("public notes"))
	for _, dir := range []string{".ssh", ".git", "project"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	private := []string{".env", ".ssh/id_ed25519", ".git/config", "project/.env", "server.pem", "tls.KEY", "prod.env", "id_rsa", "vault.kdbx"}
	for _, name := range private {
		writeFile(t, filepath.Join(root, name), []byte("MUST NEVER RETURN THIS"))
	}
	// A visible name must not reach a hidden file through a symlink.
	if err := os.Symlink(".env", filepath.Join(root, "settings.txt")); err != nil {
		t.Fatal(err)
	}
	registry := fileRegistry(t, root)
	for _, name := range append(private, "settings.txt") {
		output := runPath(registry, "read_file", filepath.Join(root, name))
		if !strings.Contains(output, "hidden") || strings.Contains(output, "MUST NEVER RETURN THIS") {
			t.Fatalf("read %s: %q", name, output)
		}
	}
	for _, dir := range []string{".ssh", ".git"} {
		if output := runPath(registry, "list_directory", filepath.Join(root, dir)); !strings.Contains(output, "hidden") || strings.Contains(output, "id_ed25519") {
			t.Fatalf("listed %s: %q", dir, output)
		}
	}
	if output := runPath(registry, "read_file", filepath.Join(root, "notes.md")); output != "public notes" {
		t.Fatal(output)
	}
	listing := runPath(registry, "list_directory", root)
	for _, name := range []string{`".env"`, `".ssh"`, `".git"`, `"server.pem"`, `"tls.KEY"`, `"prod.env"`, `"id_rsa"`, `"vault.kdbx"`} {
		if strings.Contains(listing, name) {
			t.Fatalf("listing shows %s: %s", name, listing)
		}
	}
	for _, expected := range []string{`"notes.md"`, `"project"`, `"settings.txt"`, "[8 hidden or key files not shown]"} {
		if !strings.Contains(listing, expected) {
			t.Fatalf("listing missing %s: %s", expected, listing)
		}
	}
}

func TestFileToolsAllowAToolFolderInsideAHiddenDirectory(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".config", "app")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(config, "settings.toml"), []byte("theme = 'dark'"))
	writeFile(t, filepath.Join(config, ".token"), []byte("MUST NEVER RETURN THIS"))
	// The broader root sees .config as hidden; the explicit hidden root allows it.
	registry := fileRegistry(t, home, config)
	if output := runPath(registry, "read_file", filepath.Join(config, "settings.toml")); output != "theme = 'dark'" {
		t.Fatal(output)
	}
	if output := runPath(registry, "list_directory", config); !strings.Contains(output, `"settings.toml"`) || strings.Contains(output, `".token"`) {
		t.Fatal(output)
	}
	if output := runPath(registry, "read_file", filepath.Join(config, ".token")); !strings.Contains(output, "hidden") {
		t.Fatal(output)
	}
	if output := runPath(fileRegistry(t, home), "read_file", filepath.Join(config, "settings.toml")); !strings.Contains(output, "hidden") {
		t.Fatal(output)
	}
}

func TestDirectoryListingFillsPastHiddenEntries(t *testing.T) {
	root := t.TempDir()
	// Visible files sit between two runs of hidden ones, so reading only the
	// first 257 entries misses them in creation, reverse or hashed order.
	hidden := func(from int) {
		for i := range MaxDirectoryEntries + 50 {
			writeFile(t, filepath.Join(root, fmt.Sprintf(".hidden-%03d", from+i)), []byte("x"))
		}
	}
	hidden(0)
	for i := range 10 {
		writeFile(t, filepath.Join(root, fmt.Sprintf("file-%02d", i)), []byte("x"))
	}
	hidden(MaxDirectoryEntries + 50)
	listing := runPath(fileRegistry(t, root), "list_directory", root)
	if strings.Count(listing, `"file-`) != 10 || strings.Contains(listing, ".hidden-") || strings.Contains(listing, "truncated") || !strings.Contains(listing, "[612 hidden or key files not shown]") {
		t.Fatal(listing)
	}
}
