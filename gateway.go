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

	// Asked of PHP before any worker starts. worker.php redefines header(), setcookie(), ... which PHP
	// only allows for disabled functions, and -prefork needs functions a PHP build may not have
	probe := exec.Command(cfg.php[0], append(append([]string{}, cfg.php[1:]...), cfg.workerScript, "probe")...)
	probe.Stderr = os.Stderr
	out, err := probe.Output()
	if err != nil {
		return nil, fmt.Errorf("probing PHP: %w", err)
	}
	var found struct {
		DisableFunctions string   `json:"disable_functions"`
		PreforkMissing   []string `json:"prefork_missing"`
	}
	if err := json.Unmarshal(out, &found); err != nil {
		return nil, fmt.Errorf("probing PHP: %w in %q", err, out)
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
	w, err := g.pool.acquire(ctx)
	done := outcome{queue: time.Since(queued)}
	if err != nil {
		http.Error(rw, "no PHP worker available", http.StatusServiceUnavailable)
		done.status = http.StatusServiceUnavailable
		return done
	}
	done.pid = w.pid

	resp, err := w.roundTrip(ctx, req)
	if err != nil {
		w.log.finish(requestID)
		g.pool.release(w, false)
		fields := []any{"request_id", requestID, "path", r.URL.Path, "worker_pid", w.pid}
		if errors.Is(err, context.DeadlineExceeded) {
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
