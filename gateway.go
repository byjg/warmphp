package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gateway translates HTTP requests into worker requests, the same job PHP-FPM's
// FastCGI frontend does, so any reverse proxy can sit in front of it.
type gateway struct {
	cfg  config
	pool *pool
}

func newGateway(cfg config) (g *gateway, err error) {
	if err := cfg.useEmbedded(); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(cfg.embeddedDir)
		}
	}()

	found, err := probePHP(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.prefork && len(found.PreforkMissing) > 0 {
		return nil, fmt.Errorf("-prefork needs PHP's pcntl and posix extensions, and this PHP lacks %s: "+
			"install them (see Requirements in the README), or run without -prefork", strings.Join(found.PreforkMissing, ", "))
	}
	cfg.disableFunctions = found.DisableFunctions

	p, err := newPool(cfg)
	if err != nil {
		return nil, err
	}
	return &gateway{cfg: cfg, pool: p}, nil
}

type probed struct {
	DisableFunctions string   `json:"disable_functions"`
	PreforkMissing   []string `json:"prefork_missing"`
	Stdin            string   `json:"stdin"`
}

// probePHP runs the PHP command once before any worker starts. worker.php redefines header(),
// setcookie(), ... which PHP only allows for disabled functions, -prefork needs functions a PHP
// build may not have, and every request goes to PHP on its stdin: the probe reads a line there.
func probePHP(cfg config) (probed, error) {
	var found probed
	command := strings.Join(cfg.php, " ")
	token := make([]byte, 8)
	_, _ = rand.Read(token)
	sent := hex.EncodeToString(token)

	probe := exec.Command(cfg.php[0], append(append([]string{}, cfg.php[1:]...), cfg.workerScript, "probe")...)
	probe.Stdin = strings.NewReader(sent + "\n")
	var stderr strings.Builder
	probe.Stderr = &stderr
	out, err := probe.Output()
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return found, fmt.Errorf("the PHP command %q failed (%w): %s", command, err, strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		return found, fmt.Errorf("the PHP command %q could not run: %w", command, err)
	}
	if err := json.Unmarshal(out, &found); err != nil {
		return found, fmt.Errorf("the PHP command %q did not run worker.php: it printed %q", command, out)
	}
	if found.Stdin != sent {
		return found, fmt.Errorf("the PHP command %q does not pass its stdin through to PHP, and the gateway sends every "+
			"request on it: a wrapper script or container must keep stdin open (docker run -i)", command)
	}
	return found, nil
}

func (g *gateway) Close() {
	g.pool.close()
	os.RemoveAll(g.cfg.embeddedDir)
}

// outcome is what ServeHTTP logs about a request
type outcome struct {
	status int
	bytes  int
	queue  time.Duration
	pid    int
	php    *phpMetrics
}

func (g *gateway) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	requestID := hex.EncodeToString(id)

	done := g.handle(rw, r, requestID)

	// The query string is left out: it often carries tokens and personal data
	attrs := []slog.Attr{
		slog.String("request_id", requestID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", done.status),
		slog.Float64("duration_ms", milliseconds(time.Since(start))),
		slog.Float64("queue_ms", milliseconds(done.queue)),
		slog.Int("bytes", done.bytes),
	}
	if done.pid != 0 {
		attrs = append(attrs, slog.Int("worker_pid", done.pid))
	}
	if done.php != nil {
		attrs = append(attrs, slog.Float64("php_ms", done.php.Milliseconds), slog.Float64("php_cpu_ms", done.php.CPUMilliseconds))
		if done.php.MemoryKB != nil {
			attrs = append(attrs, slog.Int("php_memory_kb", *done.php.MemoryKB))
		}
	}
	if trace := traceID(r.Header.Get("Traceparent")); trace != "" {
		attrs = append(attrs, slog.String("trace_id", trace))
	}
	level := slog.LevelInfo
	if done.status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	slog.LogAttrs(context.Background(), level, "request", attrs...)
}

func milliseconds(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

func (g *gateway) handle(rw http.ResponseWriter, r *http.Request, requestID string) outcome {
	req, tmpFiles, err := g.buildRequest(rw, r)
	defer func() {
		for _, name := range tmpFiles {
			os.Remove(name)
		}
	}()
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
			return outcome{status: http.StatusRequestEntityTooLarge}
		}
		http.Error(rw, "bad request", http.StatusBadRequest)
		return outcome{status: http.StatusBadRequest}
	}
	req.ID = requestID

	// A client disconnecting must not kill a warm worker in the middle of a request
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), g.cfg.timeout)
	defer cancel()

	queued := time.Now()
	var w *worker
	var resp *phpResponse
	for {
		if w, err = g.pool.acquire(ctx); err != nil {
			http.Error(rw, "no PHP worker available", http.StatusServiceUnavailable)
			return outcome{status: http.StatusServiceUnavailable, queue: time.Since(queued)}
		}
		resp, err = w.roundTrip(ctx, req)
		if !errors.Is(err, errNotSent) {
			break
		}
		// The worker had ended while idle (a forked one is only seen to end here): PHP never got
		// the request, so another worker takes it
		w.log.finish(requestID)
		g.pool.release(w, false)
	}
	done := outcome{queue: time.Since(queued), pid: w.pid}
	if err != nil {
		w.log.finish(requestID)
		w.timedOut = errors.Is(err, context.DeadlineExceeded)
		g.pool.release(w, false)
		fields := []any{"request_id", requestID, "path", r.URL.Path, "worker_pid", w.pid}
		if w.timedOut {
			slog.Error("request timed out", append(fields, "timeout", g.cfg.timeout.String())...)
			http.Error(rw, "PHP worker timed out", http.StatusGatewayTimeout)
			done.status = http.StatusGatewayTimeout
		} else {
			slog.Error("PHP worker failed", append(fields, "error", err.Error())...)
			http.Error(rw, "PHP worker failed", http.StatusBadGateway)
			done.status = http.StatusBadGateway
		}
		return done
	}
	w.served++
	g.pool.release(w, !resp.Exit)

	for _, header := range resp.Headers {
		if name, value, ok := strings.Cut(header, ":"); ok {
			rw.Header().Add(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}
	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	rw.WriteHeader(resp.Status)
	_, _ = rw.Write(resp.Body)
	done.status, done.bytes, done.php = resp.Status, len(resp.Body), resp.Metrics
	return done
}

// buildRequest reads the body and, for multipart POSTs, writes the uploaded files
// to cfg.uploadDir. It returns the temporary files to delete after the response.
func (g *gateway) buildRequest(rw http.ResponseWriter, r *http.Request) (*phpRequest, []string, error) {
	req := &phpRequest{Server: serverVars(r)}
	r.Body = http.MaxBytesReader(rw, r.Body, g.cfg.maxBody)

	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Method != http.MethodPost || mediaType != "multipart/form-data" {
		body, err := io.ReadAll(r.Body)
		req.Body = body
		return req, nil, err
	}

	var tmpFiles []string
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, err
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return req, tmpFiles, nil
		}
		if err != nil {
			return nil, tmpFiles, err
		}
		if part.FormName() == "" {
			continue
		}

		_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename, isFile := params["filename"]
		if !isFile {
			value, err := io.ReadAll(part)
			if err != nil {
				return nil, tmpFiles, err
			}
			req.Fields = append(req.Fields, [2]string{part.FormName(), string(value)})
			continue
		}

		file := upload{Field: part.FormName(), Name: part.FileName(), Type: part.Header.Get("Content-Type")}
		if filename == "" {
			file.Error = 4 // UPLOAD_ERR_NO_FILE: the form was sent without choosing a file
			req.Files = append(req.Files, file)
			continue
		}
		tmp, err := os.CreateTemp(g.cfg.uploadDir, "php")
		if err != nil {
			return nil, tmpFiles, err
		}
		tmpFiles = append(tmpFiles, tmp.Name())
		file.Size, err = io.Copy(tmp, part)
		tmp.Close()
		if err != nil {
			return nil, tmpFiles, err
		}
		file.TmpName = tmp.Name()
		req.Files = append(req.Files, file)
	}
}

// serverVars builds the CGI/1.1 variables (RFC 3875) PHP exposes in $_SERVER.
func serverVars(r *http.Request) map[string]string {
	vars := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1",
		"SERVER_SOFTWARE":   "warmphp",
		"SERVER_PROTOCOL":   r.Proto,
		"REQUEST_METHOD":    r.Method,
		"REQUEST_URI":       r.RequestURI,
		"QUERY_STRING":      r.URL.RawQuery,
		"HTTP_HOST":         r.Host,
	}

	for name, values := range r.Header {
		separator := ", "
		if name == "Cookie" {
			separator = "; "
		}
		vars["HTTP_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_"))] = strings.Join(values, separator)
	}
	// Content headers have their own CGI names; HTTP_PROXY would enable httpoxy
	delete(vars, "HTTP_CONTENT_TYPE")
	delete(vars, "HTTP_CONTENT_LENGTH")
	delete(vars, "HTTP_PROXY")
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		vars["CONTENT_TYPE"] = contentType
	}
	if r.ContentLength > 0 {
		vars["CONTENT_LENGTH"] = strconv.FormatInt(r.ContentLength, 10)
	}

	if host, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		vars["REMOTE_ADDR"], vars["REMOTE_PORT"] = host, port
	}
	vars["SERVER_NAME"] = r.Host
	if host, _, err := net.SplitHostPort(r.Host); err == nil {
		vars["SERVER_NAME"] = host
	}
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, port, err := net.SplitHostPort(addr.String()); err == nil {
			vars["SERVER_PORT"] = port
		}
	}
	if r.TLS != nil {
		vars["HTTPS"] = "on"
	}
	return vars
}
