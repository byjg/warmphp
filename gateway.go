package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// gateway translates HTTP requests into worker requests, the same job PHP-FPM's
// FastCGI frontend does, so any reverse proxy can sit in front of it.
type gateway struct {
	cfg  config
	pool *pool
}

func newGateway(cfg config) (*gateway, error) {
	// worker.php redefines header(), setcookie(), ... which PHP only allows for disabled functions
	probe := exec.Command(cfg.php[0], append(append([]string{}, cfg.php[1:]...), cfg.workerScript, "disable-functions")...)
	probe.Stderr = os.Stderr
	out, err := probe.Output()
	if err != nil {
		return nil, fmt.Errorf("reading disable_functions from the worker: %w", err)
	}
	cfg.disableFunctions = strings.TrimSpace(string(out))

	p, err := newPool(cfg)
	if err != nil {
		return nil, err
	}
	return &gateway{cfg: cfg, pool: p}, nil
}

func (g *gateway) Close() {
	g.pool.close()
}

func (g *gateway) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	req, tmpFiles, err := g.buildRequest(rw, r)
	defer func() {
		for _, name := range tmpFiles {
			os.Remove(name)
		}
	}()
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}

	// A client disconnecting must not kill a warm worker in the middle of a request
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), g.cfg.timeout)
	defer cancel()

	w, err := g.pool.acquire(ctx)
	if err != nil {
		http.Error(rw, "no PHP worker available", http.StatusServiceUnavailable)
		return
	}

	resp, err := w.roundTrip(ctx, req)
	if err != nil {
		g.pool.release(w, false)
		log.Printf("%s %s: %v", r.Method, r.RequestURI, err)
		if errors.Is(err, context.DeadlineExceeded) {
			http.Error(rw, "PHP worker timed out", http.StatusGatewayTimeout)
		} else {
			http.Error(rw, "PHP worker failed", http.StatusBadGateway)
		}
		return
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
		"SERVER_SOFTWARE":   "phpworker-gateway",
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
