package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// maxFrame limits a frame and a response body. Printable bytes read as a frame length are
// above it, so output in the wrong place is detected immediately.
const maxFrame = 256 << 20

// stopGrace is how long a worker has to exit before it is killed.
const stopGrace = 5 * time.Second

type phpRequest struct {
	// Boundary ends the response body on the worker's stdout. It is random for every
	// request, so a body cannot contain it by chance.
	Boundary string            `json:"boundary"`
	Server   map[string]string `json:"server"`
	Body     []byte            `json:"body"`
	Fields   [][2]string       `json:"fields,omitempty"`
	Files    []upload          `json:"files,omitempty"`
}

type upload struct {
	Field   string `json:"field"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	TmpName string `json:"tmp_name"`
	Error   int    `json:"error"`
	Size    int64  `json:"size"`
}

type phpResponse struct {
	Status  int      `json:"status"`
	Headers []string `json:"headers"`
	Body    []byte   `json:"-"` // read from stdout before the frame
	Exit    bool     `json:"exit"`
}

func writeFrame(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(data)), uint32(len(data)))
	_, err = w.Write(append(frame, data...))
	return err
}

func readFrame(r io.Reader, v any) error {
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		return err
	}
	if size > maxFrame {
		return fmt.Errorf("invalid frame size %d (stray output on the worker's stdout?)", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// readResponse reads what the worker writes to stdout for one request: the body, as in a
// web SAPI, then the boundary, then the frame with the status and the headers.
func readResponse(r io.Reader, boundary []byte) (*phpResponse, error) {
	var body []byte
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		// Only the new bytes, and the end of the old ones the boundary may start in, are searched
		from := max(0, len(body)-len(boundary)+1)
		body = append(body, chunk[:n]...)
		if i := bytes.Index(body[from:], boundary); i >= 0 {
			end := from + i
			// What was read after the boundary is the start of the frame
			rest := bytes.NewReader(body[end+len(boundary):])
			var resp phpResponse
			if err := readFrame(io.MultiReader(rest, r), &resp); err != nil {
				return nil, err
			}
			if rest.Len() > 0 {
				return nil, errors.New("output after the response frame")
			}
			resp.Body = body[:end]
			return &resp, nil
		}
		if err != nil {
			return nil, err
		}
		if len(body) > maxFrame {
			return nil, fmt.Errorf("response body larger than %d bytes", maxFrame)
		}
	}
}

// worker is one long-running `php worker.php` process. It serves one request at a time.
type worker struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	reader *bufio.Reader
	exited chan struct{}
	served int
}

func startWorker(cfg config) (*worker, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, err
	}

	args := append(append([]string{}, cfg.php[1:]...), "-d", "disable_functions="+cfg.disableFunctions, cfg.workerScript, cfg.mode, cfg.entry)
	cmd := exec.Command(cfg.php[0], args...)
	// Plain *os.File pipes: Wait() never closes our ends, so the last frame of an
	// exiting worker (exit() or fatal error) can still be read.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, os.Stderr
	err = cmd.Start()
	stdinR.Close()
	stdoutW.Close()
	if err != nil {
		stdinW.Close()
		stdoutR.Close()
		return nil, err
	}

	w := &worker{cmd: cmd, stdin: stdinW, stdout: stdoutR, reader: bufio.NewReader(stdoutR), exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(w.exited)
	}()

	ready := make(chan error, 1)
	go func() {
		var msg struct {
			Ready bool `json:"ready"`
		}
		err := readFrame(w.reader, &msg)
		if err == nil && !msg.Ready {
			err = errors.New("unexpected first message")
		}
		ready <- err
	}()

	select {
	case err = <-ready:
	case <-time.After(cfg.bootTimeout):
		err = errors.New("timed out waiting for the worker to boot")
	}
	if err != nil {
		w.stop(false)
		return nil, fmt.Errorf("starting PHP worker: %w", err)
	}
	return w, nil
}

func (w *worker) roundTrip(ctx context.Context, req *phpRequest) (*phpResponse, error) {
	type result struct {
		resp *phpResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		boundary := make([]byte, 16)
		_, _ = rand.Read(boundary)
		req.Boundary = "--phpworker-" + hex.EncodeToString(boundary)
		if err := writeFrame(w.stdin, req); err != nil {
			done <- result{err: err}
			return
		}
		resp, err := readResponse(w.reader, []byte(req.Boundary))
		done <- result{resp, err}
	}()

	select {
	case r := <-done:
		return r.resp, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *worker) dead() bool {
	select {
	case <-w.exited:
		return true
	default:
		return false
	}
}

// stop ends the worker: graceful closes stdin so the PHP loop ends on its own,
// otherwise it is sent SIGTERM. Either way it is killed after stopGrace.
func (w *worker) stop(graceful bool) {
	if graceful {
		w.stdin.Close()
	} else {
		_ = w.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-w.exited:
	case <-time.After(stopGrace):
		_ = w.cmd.Process.Kill()
		<-w.exited
	}
	w.stdin.Close()
	w.stdout.Close()
}

// pool keeps cfg.workers PHP processes alive and hands out idle ones.
type pool struct {
	cfg     config
	idle    chan *worker
	closing atomic.Bool
	pending sync.WaitGroup // workers being replaced
}

func newPool(cfg config) (*pool, error) {
	p := &pool{cfg: cfg, idle: make(chan *worker, cfg.workers)}
	errs := make(chan error, cfg.workers)
	for range cfg.workers {
		go func() {
			w, err := startWorker(cfg)
			if err == nil {
				p.idle <- w
			}
			errs <- err
		}()
	}
	var err error
	for range cfg.workers {
		err = errors.Join(err, <-errs)
	}
	if err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (p *pool) acquire(ctx context.Context) (*worker, error) {
	for {
		select {
		case w := <-p.idle:
			if !w.dead() {
				return w, nil
			}
			p.release(w, false)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// release returns w to the pool, or replaces it when it is unhealthy
// or has served cfg.maxRequests requests.
func (p *pool) release(w *worker, healthy bool) {
	recycle := p.cfg.maxRequests > 0 && w.served >= p.cfg.maxRequests
	if healthy && !recycle && !p.closing.Load() {
		p.idle <- w
		return
	}

	p.pending.Add(1)
	go func() {
		defer p.pending.Done()
		w.stop(healthy)
		p.respawn()
	}()
}

func (p *pool) respawn() {
	for delay := 100 * time.Millisecond; !p.closing.Load(); delay = min(delay*2, 5*time.Second) {
		w, err := startWorker(p.cfg)
		if err == nil {
			if p.closing.Load() {
				w.stop(true)
				return
			}
			p.idle <- w
			return
		}
		log.Printf("%v (retrying in %s)", err, delay)
		time.Sleep(delay)
	}
}

// close stops every worker. Call it after the HTTP server stopped serving requests.
func (p *pool) close() {
	p.closing.Store(true)
	p.pending.Wait()

	var stopping sync.WaitGroup
	for {
		select {
		case w := <-p.idle:
			stopping.Go(func() { w.stop(true) })
		default:
			stopping.Wait()
			return
		}
	}
}
