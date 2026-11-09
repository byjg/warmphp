package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeFile writes a file and moves its time forward, so the change shows even on a file system
// that keeps whole seconds
func writeFile(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotWatchesTheCodeNotWhatTheApplicationWrites(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"src/Controller.php", "config/dev/credentials.env", ".env.local", "public/docs/openapi.json",
		"var/cache/container.php", "storage/framework/views/a.php", "vendor/acme/lib/Lib.php", "node_modules/x/index.php",
		"vendor/composer/installed.json", "public/style.css", "README.md"} {
		writeFile(t, filepath.Join(root, name), "x", 0)
	}

	var got []string
	for path := range snapshot(root) {
		rel, _ := filepath.Rel(root, path)
		got = append(got, rel)
	}
	slices.Sort(got)
	want := []string{".env.local", "config/dev/credentials.env", "public/docs/openapi.json", "src/Controller.php", "vendor/composer/installed.json"}
	if !slices.Equal(got, want) {
		t.Fatalf("watched %v, want %v", got, want)
	}
}

func TestChangedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "kept.php"), "a", -time.Hour)
	writeFile(t, filepath.Join(root, "edited.php"), "a", -time.Hour)
	writeFile(t, filepath.Join(root, "removed.php"), "a", -time.Hour)
	before := snapshot(root)
	if changed := changedFiles(before, snapshot(root)); len(changed) != 0 {
		t.Fatalf("nothing changed, got %v", changed)
	}

	writeFile(t, filepath.Join(root, "edited.php"), "b", 0)
	writeFile(t, filepath.Join(root, "added.php"), "a", 0)
	if err := os.Remove(filepath.Join(root, "removed.php")); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, path := range changedFiles(before, snapshot(root)) {
		got = append(got, filepath.Base(path))
	}
	if want := []string{"added.php", "edited.php", "removed.php"}; !slices.Equal(got, want) {
		t.Fatalf("changed %v, want %v", got, want)
	}
}

func TestProjectRootIsTheFolderWithComposerJSON(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "composer.json"), "{}", 0)
	writeFile(t, filepath.Join(root, "public", "app.php"), "<?php", 0)
	if got := projectRoot(filepath.Join(root, "public", "app.php")); got != root {
		t.Fatalf("got %s, want %s", got, root)
	}

	alone := t.TempDir()
	if got := projectRoot(filepath.Join(alone, "app.php")); got != alone {
		t.Fatalf("an entry with no composer.json up the tree: got %s, want its own folder %s", got, alone)
	}
}

func TestWatchRefusesPrefork(t *testing.T) {
	cfg := testConfig(t, "callable", "testdata/app.php")
	cfg.prefork, cfg.watch, cfg.watchRoot = true, true, t.TempDir()
	_, err := newGateway(cfg)
	if err == nil || !strings.Contains(err.Error(), "-watch can't be used with -mode prefork") {
		t.Fatalf("expected -watch to be refused with -mode prefork, got %v", err)
	}
}

// watchApp writes an application whose answer comes from version.php, and starts a gateway on it
// with one worker, unless configure changes that. The entry is app.php (-mode worker) or index.php
// (-mode legacy). With ?sleep, app.php answers after a second.
func watchApp(t *testing.T, mode string, watch bool, configure ...func(*config)) (string, *gateway, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "version.php"), "<?php function version(): string { return 'v1'; }", -time.Hour)
	// worker: version.php is read once, when the worker boots
	writeFile(t, filepath.Join(root, "app.php"),
		"<?php require_once __DIR__ . '/version.php'; return static function (): void { "+
			"if (isset($_GET['sleep'])) { sleep(1); } echo version(); };", -time.Hour)
	// legacy: index.php is included on every request, but version.php only once per worker
	writeFile(t, filepath.Join(root, "index.php"),
		"<?php require_once __DIR__ . '/version.php'; echo version();", -time.Hour)

	entry := "app.php"
	if mode == "script" {
		entry = "index.php"
	}
	cfg := testConfig(t, mode, filepath.Join(root, entry))
	cfg.watch, cfg.watchRoot, cfg.watchInterval = watch, root, 50*time.Millisecond
	for _, change := range configure {
		change(&cfg)
	}
	g, err := newGateway(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(func() {
		srv.Close()
		g.Close()
	})
	return root, g, srv
}

func version(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	resp, body := send(t, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	return body
}

// waitForChange waits until the pool saw a change
func waitForChange(t *testing.T, g *gateway) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); g.pool.generation.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("-watch did not see the change")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Without -watch a worker keeps the code it booted with, in both modes: legacy included, as
// version.php is loaded once per worker (require_once)
func TestWithoutWatchTheWorkerKeepsTheOldCode(t *testing.T) {
	for _, mode := range []string{"callable", "script"} {
		t.Run(mode, func(t *testing.T) {
			root, _, srv := watchApp(t, mode, false)
			if got := version(t, srv); got != "v1" {
				t.Fatalf("got %q, want v1", got)
			}
			writeFile(t, filepath.Join(root, "version.php"), "<?php function version(): string { return 'v2'; }", 0)
			time.Sleep(200 * time.Millisecond)
			if got := version(t, srv); got != "v1" {
				t.Fatalf("got %q: the worker was expected to keep v1 without -watch", got)
			}
		})
	}
}

// With -watch, the first request after the change runs the new code: the worker that booted with
// the old one is never handed it
func TestWatchReplacesTheWorkersWhenTheCodeChanges(t *testing.T) {
	for _, mode := range []string{"callable", "script"} {
		t.Run(mode, func(t *testing.T) {
			root, g, srv := watchApp(t, mode, true)
			if got := version(t, srv); got != "v1" {
				t.Fatalf("got %q, want v1", got)
			}
			writeFile(t, filepath.Join(root, "version.php"), "<?php function version(): string { return 'v2'; }", 0)
			waitForChange(t, g)
			if got := version(t, srv); got != "v2" {
				t.Fatalf("got %q, want v2 on the first request after the change", got)
			}
		})
	}
}

// A file the application writes while it serves (its cache) does not restart the workers
func TestWatchIgnoresWhatTheApplicationWrites(t *testing.T) {
	root, g, srv := watchApp(t, "callable", true)
	version(t, srv)
	writeFile(t, filepath.Join(root, "var", "cache", "compiled.php"), "<?php return [];", 0)
	time.Sleep(300 * time.Millisecond)
	if gen := g.pool.generation.Load(); gen != 0 {
		t.Fatalf("a write to var/ started generation %d", gen)
	}
}

func TestDescribeFilesNamesAFewAndCountsTheRest(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "app")
	files := func(names ...string) []string {
		var paths []string
		for _, name := range names {
			paths = append(paths, filepath.Join(root, name))
		}
		return paths
	}
	for _, c := range []struct {
		files []string
		want  string
	}{
		{files("src/A.php"), filepath.Join("src", "A.php")},
		{files("a.php", "b.php", "c.php", "d.php", "e.php"), "a.php, b.php, c.php, d.php, e.php"},
		{files("a.php", "b.php", "c.php", "d.php", "e.php", "f.php", "g.php"), "a.php, b.php, c.php, d.php, e.php and 2 more"},
	} {
		if got := describeFiles(root, c.files); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// The change is logged with the files, and once every worker runs the new code, a summary says so:
// a worker busy with a request at the change is replaced after it, so the summary waits for it
func TestWatchSaysWhenEveryWorkerRunsTheNewCode(t *testing.T) {
	logs := captureLogs(t)
	root, g, srv := watchApp(t, "callable", true, func(cfg *config) { cfg.workers = 2 })

	busy := make(chan string)
	go func() {
		resp, err := http.Get(srv.URL + "/?sleep")
		if err != nil {
			busy <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		busy <- string(body)
	}()
	time.Sleep(300 * time.Millisecond) // the request is in PHP's sleep
	writeFile(t, filepath.Join(root, "version.php"), "<?php function version(): string { return 'v2'; }", 0)
	waitForChange(t, g)

	changed := logs.message(t, "code changed")
	if changed["files"] != float64(1) || changed["changed"] != "version.php" {
		t.Fatalf("the change was logged as %v", changed)
	}
	if reloaded := logs.all(t); slices.ContainsFunc(reloaded, func(r map[string]any) bool { return strings.HasPrefix(r["msg"].(string), "reloaded") }) {
		t.Fatal("reloaded was logged while a worker still served the old code")
	}
	if got := <-busy; got != "v1" {
		t.Fatalf("the request that started before the change got %q, want v1", got)
	}
	summary := logs.message(t, "reloaded")
	if summary["changed"] != "version.php" || summary["workers"] != float64(2) || summary["ms"].(float64) < 500 {
		t.Fatalf("the summary is %v: it should follow the busy worker, half a second or more after the change", summary)
	}
	if got := version(t, srv); got != "v2" {
		t.Fatalf("got %q after the summary, want v2", got)
	}
}
