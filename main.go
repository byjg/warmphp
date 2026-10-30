package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type config struct {
	listen       string
	php          []string
	workerScript string
	mode         string
	entry        string
	workers      int
	maxRequests  int
	timeout      time.Duration
	bootTimeout  time.Duration
	maxBody      int64
	uploadDir    string

	// disableFunctions is the disable_functions value workers start with (see worker.php)
	disableFunctions string
}

func main() {
	var cfg config
	var php string
	flag.StringVar(&cfg.listen, "listen", ":8080", "address to listen on")
	flag.StringVar(&php, "php", "php", "command that runs the PHP CLI; may include arguments, e.g. \"docker run --rm -i --init ... php\"")
	flag.StringVar(&cfg.workerScript, "worker", "php/worker.php", "path to worker.php")
	flag.StringVar(&cfg.mode, "mode", "callable", "callable: entry runs once and returns the handler; script: entry is included on every request")
	flag.IntVar(&cfg.workers, "workers", runtime.NumCPU(), "number of PHP worker processes")
	flag.IntVar(&cfg.maxRequests, "max-requests", 0, "replace a worker after this many requests (0 = never)")
	flag.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "maximum time to wait for a worker and process a request")
	flag.DurationVar(&cfg.bootTimeout, "boot-timeout", 30*time.Second, "maximum time for a worker to boot")
	flag.Int64Var(&cfg.maxBody, "max-body", 32<<20, "maximum request body size in bytes")
	flag.StringVar(&cfg.uploadDir, "upload-dir", os.TempDir(), "directory for uploaded files; must be visible to PHP")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s [flags] entry.php\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 || (cfg.mode != "callable" && cfg.mode != "script") || cfg.workers < 1 {
		flag.Usage()
		os.Exit(2)
	}
	cfg.php = strings.Fields(php)
	var err error
	if cfg.entry, err = filepath.Abs(flag.Arg(0)); err != nil {
		log.Fatal(err)
	}
	if cfg.workerScript, err = filepath.Abs(cfg.workerScript); err != nil {
		log.Fatal(err)
	}

	g, err := newGateway(cfg)
	if err != nil {
		log.Fatal(err)
	}

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

	log.Printf("listening on %s with %d %s workers for %s", cfg.listen, cfg.workers, cfg.mode, cfg.entry)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-stopped
	g.Close()
}
