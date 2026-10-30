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
	BodySize int               `json:"body_size"`
	Body     []byte            `json:"-"` // written raw after the frame
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

// writeFrame writes v as a frame, then payload as is: the request body never goes through JSON
func writeFrame(w io.Writer, v any, payload []byte) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(data)+len(payload)), uint32(len(data)))
	_, err = w.Write(append(append(frame, data...), payload...))
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

// worker is one long-running PHP process. It serves one request at a time. It is either a
// `php worker.php` process of its own (cmd, talking over its stdin and stdout), or a process the
// master forked (-prefork, talking over a Unix socket connection).
type worker struct {
	cmd    *exec.Cmd // nil for a forked worker
	in     io.WriteCloser
	out    io.Closer
	reader *bufio.Reader
	exited chan struct{} // nil for a forked worker: its errors show up on the connection
	kill   func()        // forked worker: asks the master to kill it
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

	w := &worker{cmd: cmd, in: stdinW, out: stdoutR, reader: bufio.NewReader(stdoutR), exited: make(chan struct{})}
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
		req.BodySize = len(req.Body)
		if err := writeFrame(w.in, req, req.Body); err != nil {
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

// stop ends the worker: graceful closes its input so the PHP loop ends on its own, otherwise it
// is sent SIGTERM (a forked worker: SIGKILL, from the master). A process of its own is killed
// after stopGrace; the master replaces a forked one.
func (w *worker) stop(graceful bool) {
	if w.cmd == nil {
		if !graceful {
			w.kill()
		}
		w.in.Close()
		return
	}
	if graceful {
		w.in.Close()
	} else {
		_ = w.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-w.exited:
	case <-time.After(stopGrace):
		_ = w.cmd.Process.Kill()
		<-w.exited
	}
	w.in.Close()
	w.out.Close()
}

// pool keeps cfg.workers PHP processes alive and hands out idle ones.
type pool struct {
	cfg     config
	idle    chan *worker
	closing atomic.Bool
	pending sync.WaitGroup // workers being replaced
	master  *master        // -prefork: it forks the workers and replaces them
}

func newPool(cfg config) (*pool, error) {
	p := &pool{cfg: cfg, idle: make(chan *worker, cfg.workers)}
	if cfg.prefork {
		return p, p.startMaster()
	}
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
		// The master forks the replacement of a forked worker, which connects on its own
		if p.master == nil {
			p.respawn()
		}
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
	if p.master != nil {
		// The master stops its workers; their connections are closed below
		p.master.stop()
	}

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
