package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The gateway logs JSON on stderr, one record per line: its own events, a record per request, and
// what PHP writes to its stderr. A collector reads them from there (see the README). The records
// go through a logWriter, so a destination that stalls never reaches the requests.

// markerStart and markerEnd start the lines the worker writes to stderr around a request:
// "\x01start <id> <pid>" and "\x01end <id>". They come through the pipe in order with PHP's own
// lines, which is how those lines are tied to a request: PHP writes them, so they cannot carry its id.
const (
	markerStart = "\x01start "
	markerEnd   = "\x01end "
)

// maxPHPLine limits a line without a newline, so stray output cannot grow without limit
const maxPHPLine = 1 << 20

const (
	logQueueSize    = 8192
	logBatchSize    = 64 << 10
	logFlushTimeout = 2 * time.Second
)

var (
	stackLine = regexp.MustCompile(`^(Stack trace:|#\d+ |  thrown in |Next )`)
	phpError  = regexp.MustCompile(`^(PHP )?(Fatal error|Parse error|Recoverable fatal error)\b`)
	phpWarn   = regexp.MustCompile(`^(PHP )?(Warning|Notice|Deprecated)\b`)
)

// logWriter writes the log in the background, in batches. Write never blocks: requests wait for
// no destination, and PHP, whose stderr goes through here too, never stalls on a full pipe. When the
// queue is full the record is dropped, and a "logs dropped" record says how many were.
type logWriter struct {
	out     io.Writer
	queue   chan []byte
	dropped atomic.Int64
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newLogWriter(out io.Writer, size int) *logWriter {
	l := &logWriter{out: out, queue: make(chan []byte, size), stop: make(chan struct{}), stopped: make(chan struct{})}
	go l.run()
	return l
}

func (l *logWriter) Write(record []byte) (int, error) {
	select {
	case l.queue <- bytes.Clone(record):
	default:
		l.dropped.Add(1)
	}
	return len(record), nil
}

func (l *logWriter) run() {
	defer close(l.stopped)
	var batch []byte
	for {
		select {
		case record := <-l.queue:
			batch = append(batch[:0], record...)
		case <-l.stop:
			batch = batch[:0]
		}
		// What is queued goes in this batch, up to its size; when stopping, all of it
		stopping := false
	drain:
		for {
			select {
			case <-l.stop:
				stopping = true
			default:
			}
			if len(batch) >= logBatchSize && !stopping {
				break
			}
			select {
			case record := <-l.queue:
				batch = append(batch, record...)
			default:
				break drain
			}
		}
		if count := l.dropped.Swap(0); count > 0 {
			batch = append(batch, droppedRecord(count)...)
		}
		_, _ = l.out.Write(batch)
		if stopping {
			return
		}
	}
}

// droppedRecord does not go through the queue, which may be full
func droppedRecord(count int64) []byte {
	var record bytes.Buffer
	slog.New(slog.NewJSONHandler(&record, nil)).Warn("logs dropped", "count", count)
	return record.Bytes()
}

// flush writes what is queued. A destination that stalled does not hold the gateway's exit: after
// timeout, what is left is lost.
func (l *logWriter) flush(timeout time.Duration) {
	l.once.Do(func() { close(l.stop) })
	select {
	case <-l.stopped:
	case <-time.After(timeout):
	}
}

// phpLog turns what PHP processes write to one stderr pipe into log records. A worker of its own
// has its pipe, so it serves one request at a time. With -prefork all the workers share the
// master's: a line is tied to a request only when one request is running, because otherwise
// nothing says whose it is, and no id is better than a wrong one.
type phpLog struct {
	mu     sync.Mutex
	active map[string]string // request id -> worker pid
}

func newPHPLog() *phpLog {
	return &phpLog{active: map[string]string{}}
}

// read logs r until it ends. PHP writes a message and its stack trace at once, so one Read gives
// the whole of it. A longer stack trace may come in two Reads, and be logged as two records.
func (l *phpLog) read(r io.Reader) {
	buf := make([]byte, 64<<10)
	var pending []byte
	for {
		n, err := r.Read(buf)
		pending = append(pending, buf[:n]...)
		if end := bytes.LastIndexByte(pending, '\n'); end >= 0 {
			l.lines(string(pending[:end]))
			pending = append(pending[:0], pending[end+1:]...)
		} else if len(pending) >= maxPHPLine {
			l.lines(string(pending))
			pending = pending[:0]
		}
		if err != nil {
			l.lines(string(pending))
			return
		}
	}
}

func (l *phpLog) lines(chunk string) {
	var message string
	var stack []string
	flush := func() {
		if message != "" {
			l.record(message, stack)
		}
		message, stack = "", nil
	}

	for line := range strings.SplitSeq(chunk, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, markerStart):
			flush()
			if id, pid, ok := strings.Cut(strings.TrimPrefix(line, markerStart), " "); ok {
				l.mu.Lock()
				l.active[id] = pid
				l.mu.Unlock()
			}
		case strings.HasPrefix(line, markerEnd):
			flush()
			l.finish(strings.TrimPrefix(line, markerEnd))
		case message != "" && stackLine.MatchString(line):
			stack = append(stack, line)
		default:
			flush()
			message = line
		}
	}
	flush()
}

// finish ends a request. The worker does it, unless it died and the gateway has to.
func (l *phpLog) finish(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.active, id)
}

func (l *phpLog) record(message string, stack []string) {
	attrs := []slog.Attr{slog.String("source", "php")}
	l.mu.Lock()
	if len(l.active) == 1 {
		for id, pid := range l.active {
			attrs = append(attrs, slog.String("request_id", id))
			if number, err := strconv.Atoi(pid); err == nil {
				attrs = append(attrs, slog.Int("worker_pid", number))
			}
		}
	}
	l.mu.Unlock()
	if stack != nil {
		attrs = append(attrs, slog.String("stack", strings.Join(stack, "\n")))
	}
	slog.LogAttrs(context.Background(), phpLevel(message), message, attrs...)
}

func phpLevel(message string) slog.Level {
	switch {
	case phpError.MatchString(message):
		return slog.LevelError
	case phpWarn.MatchString(message):
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

// logOutput is the log of the running gateway
var logOutput *logWriter

// traceID reads the trace id of a W3C traceparent header: version-traceid-parentid-flags
func traceID(traceparent string) string {
	fields := strings.Split(traceparent, "-")
	if len(fields) < 4 || len(fields[1]) != 32 || fields[1] == strings.Repeat("0", 32) {
		return ""
	}
	if _, err := strconv.ParseUint(fields[1][:16], 16, 64); err != nil {
		return ""
	}
	if _, err := strconv.ParseUint(fields[1][16:], 16, 64); err != nil {
		return ""
	}
	return fields[1]
}

// fatal logs err and stops the gateway
func fatal(err error) {
	slog.Error(err.Error())
	if logOutput != nil {
		logOutput.flush(logFlushTimeout)
	}
	os.Exit(1)
}
