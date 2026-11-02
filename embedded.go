package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// The binary carries worker.php and the entries of compat/. PHP can only run real files, so they
// are written to a folder when the gateway starts.
//
//go:embed php compat
var embedded embed.FS

// frameworks are the entries of compat/ that -framework selects
var frameworks = []string{"cakephp", "codeigniter", "gluo", "laravel-fresh", "laravel-shared", "symfony"}

// extractLock keeps a PHP process from starting while another goroutine is writing a file it needs
var extractLock sync.Mutex

// extract writes the embedded files that dir does not have
func extract(dir string) error {
	extractLock.Lock()
	defer extractLock.Unlock()
	return fs.WalkDir(embedded, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, path)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return nil
		}
		data, err := embedded.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// extractTo is -extract: it writes the embedded files for the user to change, so it never
// overwrites a folder that has something in it
func extractTo(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	}
	return extract(dir)
}

// useEmbedded points the worker script and the framework's entry to the embedded files, when
// they are the ones in use. The folder is in the upload directory because PHP must see it (the
// PHP command may be a container), and has a random name because that directory is usually /tmp,
// where a known name could be created first by another user.
func (c *config) useEmbedded() error {
	if c.workerScript != "" && c.framework == "" {
		return nil
	}
	dir, err := os.MkdirTemp(c.uploadDir, "warmphp-")
	if err != nil {
		return err
	}
	// PHP may run as another user, in a container
	if err := os.Chmod(dir, 0o755); err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err := extract(dir); err != nil {
		os.RemoveAll(dir)
		return err
	}
	c.embeddedDir = dir
	if c.workerScript == "" {
		c.workerScript = filepath.Join(dir, "php", "worker.php")
	}
	if c.framework != "" {
		c.entry = filepath.Join(dir, "compat", c.framework+".php")
	}
	return nil
}

// phpCommand is the command line of a worker or of the master. A gateway runs for weeks, and
// systemd-tmpfiles and the like delete what is old in /tmp, so the embedded files are put back
// before every start.
func (c config) phpCommand(args ...string) ([]string, error) {
	if c.embeddedDir != "" {
		if err := extract(c.embeddedDir); err != nil {
			return nil, err
		}
	}
	command := append([]string{}, c.php...)
	command = append(command, "-d", "disable_functions="+c.disableFunctions, c.workerScript, c.mode, c.entry)
	command = append(command, args...)
	if c.projectRoot != "" {
		command = append(command, "--project-root="+c.projectRoot)
	}
	return command, nil
}
