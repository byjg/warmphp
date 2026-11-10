package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
)

type config struct {
	listen       string
	php          []string
	workerScript string
	// mode is how worker.php runs the entry: "callable" (it returns the request handler) or "script"
	// (it is included on every request). The -mode flag sets it with prefork (see parseMode).
	mode        string
	entry       string
	framework   string
	projectRoot string
	workers     int
	maxRequests int
	prefork     bool
	timeout     time.Duration
	bootTimeout time.Duration
	maxBody     int64
	uploadDir   string
	// watch (-watch): replace the workers when a file under watchRoot changes, polled every
	// watchInterval (watchInterval when 0)
	watch         bool
	watchRoot     string
	watchInterval time.Duration

	// disableFunctions is the disable_functions value workers start with (see worker.php)
	disableFunctions string
	// embeddedDir is where the embedded files were written, when they are in use (see embedded.go)
	embeddedDir string
}

// buildVersion is set at build time (make build, the release); a plain go build says dev
var buildVersion = "dev"

func main() {
	// A log destination that exits must not stop the gateway: Go ends a program on a broken stderr
	signal.Ignore(syscall.SIGPIPE)
	logOutput = newLogWriter(os.Stderr, logQueueSize)
	slog.SetDefault(slog.New(slog.NewJSONHandler(logOutput, nil)))

	var cfg config
	var php, extractDir, mode string
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "print the version and exit")
	flag.StringVar(&cfg.listen, "listen", ":8080", "address to listen on")
	flag.StringVar(&php, "php", "php", "command that runs the PHP CLI; may include arguments, e.g. \"docker run --rm -i --init ... php\"")
	flag.StringVar(&cfg.workerScript, "worker", "", "path to a worker.php to use instead of the embedded one")
	flag.StringVar(&cfg.framework, "framework", "", "run the embedded entry of this framework instead of an entry file: "+strings.Join(frameworks, ", "))
	flag.StringVar(&cfg.projectRoot, "project-root", "", "the application's folder, given to PHP as PROJECT_ROOT; overrides that variable")
	flag.StringVar(&extractDir, "extract", "", "write the embedded PHP files to this folder, which must be empty, and exit")
	flag.StringVar(&mode, "mode", "worker", "worker: each worker boots the entry once, which returns the request handler; "+
		"prefork: as worker, but booted once in a master that forks the workers (needs pcntl and posix); "+
		"legacy: the entry is a front controller, included on every request in a worker that stays loaded")
	flag.IntVar(&cfg.workers, "workers", runtime.NumCPU(), "number of PHP worker processes")
	flag.IntVar(&cfg.maxRequests, "max-requests", 0, "replace a worker after this many requests (0 = never)")
	flag.BoolVar(&cfg.watch, "watch", false, "development: replace the workers when a file of the application changes, "+
		"so the next request runs the new code (not with -mode prefork)")
	flag.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "maximum time to wait for a worker and process a request")
	flag.DurationVar(&cfg.bootTimeout, "boot-timeout", 30*time.Second, "maximum time for a worker to boot")
	flag.Int64Var(&cfg.maxBody, "max-body", 32<<20, "maximum request body size in bytes")
	flag.StringVar(&cfg.uploadDir, "upload-dir", os.TempDir(), "directory for uploaded files; must be visible to PHP")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %[1]s [flags] entry.php\n       %[1]s [flags] -framework name\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Println("warmphp", buildVersion)
		return
	}

	if extractDir != "" {
		if err := extractTo(extractDir); err != nil {
			fatal(err)
		}
		return
	}

	wantedArgs := 1
	if cfg.framework != "" {
		wantedArgs = 0
	}
	if err := parseMode(mode, &cfg); err != nil {
		fmt.Fprintln(flag.CommandLine.Output(), err)
		flag.Usage()
		os.Exit(2)
	}
	if flag.NArg() != wantedArgs || cfg.workers < 1 ||
		(cfg.framework != "" && (!slices.Contains(frameworks, cfg.framework) || cfg.mode != "callable")) {
		flag.Usage()
		os.Exit(2)
	}
	cfg.php = strings.Fields(php)
	for _, path := range []*string{&cfg.workerScript, &cfg.projectRoot} {
		if *path != "" {
			var err error
			if *path, err = filepath.Abs(*path); err != nil {
				fatal(err)
			}
		}
	}
	if cfg.framework == "" {
		var err error
		if cfg.entry, err = filepath.Abs(flag.Arg(0)); err != nil {
			fatal(err)
		}
	}
	if cfg.watch {
		// The application's folder: -project-root (or PROJECT_ROOT, as the compat/ entries read it),
		// otherwise the nearest folder with a composer.json up from the entry
		switch {
		case cfg.projectRoot != "":
			cfg.watchRoot = cfg.projectRoot
		case os.Getenv("PROJECT_ROOT") != "":
			cfg.watchRoot, _ = filepath.Abs(os.Getenv("PROJECT_ROOT"))
		case cfg.entry != "":
			cfg.watchRoot = projectRoot(cfg.entry)
		default:
			fatal(errors.New("-watch with -framework needs -project-root"))
		}
	}

	g, err := newGateway(cfg)
	if err != nil {
		fatal(err)
	}
	go func() { fatal(<-g.pool.failed) }()

	srv := &http.Server{Addr: cfg.listen, Handler: g}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		close(stopped)
	}()

	slog.Info("listening", "address", cfg.listen, "workers", cfg.workers, "mode", mode, "watch", cfg.watch, "entry", g.cfg.entry)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		g.Close()
		fatal(err)
	}
	<-stopped
	g.Close()
	logOutput.flush(logFlushTimeout)
}

// parseMode sets how worker.php runs the entry and whether a master forks the workers, from -mode
func parseMode(mode string, cfg *config) error {
	switch mode {
	case "worker":
		cfg.mode, cfg.prefork = "callable", false
	case "prefork":
		cfg.mode, cfg.prefork = "callable", true
	case "legacy":
		cfg.mode, cfg.prefork = "script", false
	default:
		return fmt.Errorf("-mode must be worker, prefork or legacy, not %q", mode)
	}
	return nil
}
