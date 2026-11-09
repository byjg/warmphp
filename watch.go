package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// -watch, for development: a worker keeps the code it booted with, so when a file of the
// application changes, the workers are replaced and the next request runs on the new code.
//
// The files are polled: no dependency, and it works on any OS and on mounted volumes, where file
// events often do not arrive. Each change starts a new generation; a worker of an older one is
// never handed a request: the idle ones are replaced at once, a busy one when its request ends.

const watchInterval = 500 * time.Millisecond

// watchSkip are the folders not watched: version control, the IDE, the frontend's packages,
// vendor/ (thousands of files: vendor/composer/installed.json stands for it), and the folders
// frameworks write to while they serve (Symfony's var/, Laravel's storage/, CodeIgniter's
// writable/, caches, logs): a write there would restart the workers on every request.
var watchSkip = []string{".git", ".idea", ".vscode", "node_modules", "vendor", "var", "storage", "writable", "tmp", "cache", "logs"}

// watchExt are the files watched: the code and the configuration it reads at boot
var watchExt = []string{".php", ".env", ".json", ".yaml", ".yml", ".ini", ".xml", ".neon"}

type fileStamp struct {
	modTime time.Time
	size    int64
}

func watchedFile(name string) bool {
	return slices.Contains(watchExt, filepath.Ext(name)) || strings.HasPrefix(name, ".env.")
}

// snapshot stamps the watched files under root
func snapshot(root string) map[string]fileStamp {
	files := map[string]fileStamp{}
	stamp := func(path string, info fs.FileInfo) {
		files[path] = fileStamp{info.ModTime(), info.Size()}
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // removed while walking, or not readable: not watched
		}
		if d.IsDir() {
			if path != root && slices.Contains(watchSkip, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if watchedFile(d.Name()) {
			if info, err := d.Info(); err == nil {
				stamp(path, info)
			}
		}
		return nil
	})
	// composer install, update and require rewrite it
	installed := filepath.Join(root, "vendor", "composer", "installed.json")
	if info, err := os.Stat(installed); err == nil {
		stamp(installed, info)
	}
	return files
}

// changedFiles lists the files added, removed or changed between two snapshots
func changedFiles(before, after map[string]fileStamp) []string {
	var changed []string
	for path, stamp := range after {
		if old, ok := before[path]; !ok || !old.modTime.Equal(stamp.modTime) || old.size != stamp.size {
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	return changed
}

// projectRoot is the folder -watch watches for an entry file: the nearest one up from the entry
// with a composer.json, or the entry's own folder
func projectRoot(entry string) string {
	start := filepath.Dir(entry)
	for dir := start; ; {
		if _, err := os.Stat(filepath.Join(dir, "composer.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// watch polls the files from the snapshot taken before the workers booted, until the pool closes
func (p *pool) watch(files map[string]fileStamp) {
	interval := p.cfg.watchInterval
	if interval <= 0 {
		interval = watchInterval
	}
	for !p.closing.Load() {
		time.Sleep(interval)
		current := snapshot(p.cfg.watchRoot)
		changed := changedFiles(files, current)
		if len(changed) == 0 || p.closing.Load() {
			continue
		}
		files = current
		p.changed(changed)
		// Only the workers idle now: a replacement joins idle too, and must not be taken again
		for range len(p.idle) {
			select {
			case w := <-p.idle:
				p.release(w, !w.dead())
			default:
			}
		}
	}
}

// changed starts a new generation: from now on, the workers that booted before are stale
func (p *pool) changed(files []string) {
	described := describeFiles(p.cfg.watchRoot, files)
	p.reload.mu.Lock()
	defer p.reload.mu.Unlock()
	p.reload.gen = p.generation.Add(1)
	p.reload.changed, p.reload.since, p.reload.ready = described, time.Now(), 0
	slog.Info("code changed, replacing the workers", "files", len(files), "changed", described)
}

// reloaded counts a worker that booted on the code of the last change, and says when all of them
// did. A worker busy at the change is replaced after its request, so that can take that long.
func (p *pool) reloaded(w *worker) {
	if !p.cfg.watch {
		return
	}
	p.reload.mu.Lock()
	defer p.reload.mu.Unlock()
	if w.gen == 0 || w.gen != p.reload.gen {
		return
	}
	if p.reload.ready++; p.reload.ready == p.cfg.workers {
		slog.Info("reloaded: every worker runs the new code", "changed", p.reload.changed,
			"workers", p.cfg.workers, "ms", milliseconds(time.Since(p.reload.since)))
	}
}

// replaced uncounts a worker of the last change that is replaced before all of them are up (it
// failed, or reached -max-requests): its replacement counts instead
func (p *pool) replaced(w *worker) {
	if !p.cfg.watch {
		return
	}
	p.reload.mu.Lock()
	defer p.reload.mu.Unlock()
	if w.gen != 0 && w.gen == p.reload.gen && p.reload.ready < p.cfg.workers {
		p.reload.ready--
	}
}

// watchNamed: the files a change names; past that, it counts them
const watchNamed = 5

// describeFiles names the changed files, relative to the watched folder: "a.php, b.php and 3 more"
func describeFiles(root string, files []string) string {
	var names []string
	for _, file := range files[:min(len(files), watchNamed)] {
		if rel, err := filepath.Rel(root, file); err == nil {
			file = rel
		}
		names = append(names, file)
	}
	described := strings.Join(names, ", ")
	if more := len(files) - len(names); more > 0 {
		described += fmt.Sprintf(" and %d more", more)
	}
	return described
}
