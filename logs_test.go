package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// logRecords collects what the gateway logs during a test
type logRecords struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logRecords) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(data)
}

func (l *logRecords) all(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(l.buf.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("not a JSON record: %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// find waits for the record with these fields: PHP's lines arrive through their own pipe
func (l *logRecords) find(t *testing.T, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, record := range l.all(t) {
			if match(record) {
				return record
			}
		}
	}
	t.Fatalf("no such record in %v", l.all(t))
	return nil
}

func (l *logRecords) request(t *testing.T, path string) map[string]any {
	t.Helper()
	return l.find(t, func(r map[string]any) bool { return r["msg"] == "request" && r["path"] == path })
}

func (l *logRecords) message(t *testing.T, text string) map[string]any {
	t.Helper()
	return l.find(t, func(r map[string]any) bool { return strings.Contains(r["msg"].(string), text) })
}

func captureLogs(t *testing.T) *logRecords {
	t.Helper()
	records := &logRecords{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(records, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return records
}

func TestRequestRecord(t *testing.T) {
	logs := captureLogs(t)
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/count?token=secret", nil)
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	resp, body := send(t, req)
	assertEqual(t, "status", resp.StatusCode, http.StatusOK)

	record := logs.request(t, "/count")
	assertEqual(t, "level", record["level"], "INFO")
	assertEqual(t, "method", record["method"], "GET")
	assertEqual(t, "status", record["status"], float64(200))
	assertEqual(t, "bytes", record["bytes"], float64(len(body)))
	assertEqual(t, "trace_id", record["trace_id"], "4bf92f3577b34da6a3ce929d0e0e4736")
	var count counter
	if err := json.Unmarshal([]byte(body), &count); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "worker_pid", record["worker_pid"], float64(count.Pid))
	if record["request_id"] == "" || record["duration_ms"].(float64) <= 0 || record["php_ms"].(float64) <= 0 {
		t.Fatalf("incomplete record: %v", record)
	}
	if strings.Contains(logs.buf.String(), "secret") {
		t.Fatal("the query string must not be logged")
	}
}

func TestMetricsMeasureEachRequest(t *testing.T) {
	logs := captureLogs(t)
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	get(t, srv.URL+"/allocate")
	get(t, srv.URL+"/count")

	allocate := logs.request(t, "/allocate")
	if memory := allocate["php_memory_kb"].(float64); memory < 5<<10 {
		t.Fatalf("/allocate used 5 MiB, the record says %v KiB", memory)
	}
	// The peak of the last request must not reach this one
	count := logs.request(t, "/count")
	if memory := count["php_memory_kb"].(float64); memory > 1024 {
		t.Fatalf("/count used %v KiB", memory)
	}
	if cpu := count["php_cpu_ms"].(float64); cpu < 0 || cpu > count["php_ms"].(float64)+5 {
		t.Fatalf("/count: CPU %v ms in %v ms", cpu, count["php_ms"])
	}
}

func TestMetricsOfAWaitingRequestShowNoCPU(t *testing.T) {
	logs := captureLogs(t)
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	get(t, srv.URL+"/sleep")

	record := logs.request(t, "/sleep")
	if ms, cpu := record["php_ms"].(float64), record["php_cpu_ms"].(float64); ms < 4900 || cpu > 100 {
		t.Fatalf("slept 5 s: %v ms with %v ms of CPU", ms, cpu)
	}
}

func TestPHPErrorsBecomeRecordsOfTheirRequest(t *testing.T) {
	for name, configure := range map[string]func(*config){"workers": func(*config) {}, "prefork": prefork(1)} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			srv := startGateway(t, "callable", "testdata/app.php", configure)

			resp, _ := get(t, srv.URL+"/throw")
			assertEqual(t, "status", resp.StatusCode, http.StatusInternalServerError)
			_, body := get(t, srv.URL+"/warning")
			assertEqual(t, "body", strings.Contains(body, "Undefined"), false)
			get(t, srv.URL+"/error-log")

			request := logs.request(t, "/throw")
			assertEqual(t, "level", request["level"], "ERROR")
			uncaught := logs.message(t, "uncaught-marker")
			assertEqual(t, "level", uncaught["level"], "ERROR")
			assertEqual(t, "source", uncaught["source"], "php")
			assertEqual(t, "request_id", uncaught["request_id"], request["request_id"])
			assertEqual(t, "worker_pid", uncaught["worker_pid"], request["worker_pid"])
			if !strings.Contains(uncaught["stack"].(string), "{main}") {
				t.Fatalf("no stack trace: %v", uncaught)
			}

			warning := logs.message(t, "Undefined variable")
			assertEqual(t, "level", warning["level"], "WARN")
			assertEqual(t, "request_id", warning["request_id"], logs.request(t, "/warning")["request_id"])

			plain := logs.message(t, "error-log-marker")
			assertEqual(t, "level", plain["level"], "INFO")
			assertEqual(t, "request_id", plain["request_id"], logs.request(t, "/error-log")["request_id"])

			count := 0
			for _, record := range logs.all(t) {
				if strings.Contains(record["msg"].(string), "Undefined variable") {
					count++
				}
			}
			assertEqual(t, "records of the warning", count, 1)
		})
	}
}

func TestBootOutputIsLoggedWithoutARequest(t *testing.T) {
	logs := captureLogs(t)
	startGateway(t, "callable", "testdata/app.php", nil)

	record := logs.message(t, "boot output must go to stderr")
	assertEqual(t, "source", record["source"], "php")
	if _, tagged := record["request_id"]; tagged {
		t.Fatalf("boot output belongs to no request: %v", record)
	}
}

func TestTimeoutIsLogged(t *testing.T) {
	logs := captureLogs(t)
	srv := startGateway(t, "callable", "testdata/app.php", func(cfg *config) { cfg.timeout = 200 * time.Millisecond })

	resp, _ := get(t, srv.URL+"/sleep")
	assertEqual(t, "status", resp.StatusCode, http.StatusGatewayTimeout)

	request := logs.request(t, "/sleep")
	assertEqual(t, "status", request["status"], float64(http.StatusGatewayTimeout))
	assertEqual(t, "request_id", logs.message(t, "request timed out")["request_id"], request["request_id"])
}

func TestRejectedRequestIsLogged(t *testing.T) {
	logs := captureLogs(t)
	srv := startGateway(t, "callable", "testdata/app.php", func(cfg *config) { cfg.maxBody = 10 })

	resp, err := http.Post(srv.URL+"/count", "text/plain", strings.NewReader(strings.Repeat("x", 100)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	assertEqual(t, "status", logs.request(t, "/count")["status"], float64(http.StatusRequestEntityTooLarge))
}

func TestPHPLogLeavesConcurrentRequestsUntagged(t *testing.T) {
	logs := captureLogs(t)
	l := newPHPLog()

	l.lines("\x01start one 10\nPHP Warning:  alone\n\x01start two 11\nPHP Warning:  shared\n\x01end two\nPHP Warning:  alone again\n\x01end one\nafter")

	assertEqual(t, "request_id", logs.message(t, "PHP Warning:  alone")["request_id"], "one")
	assertEqual(t, "request_id", logs.message(t, "alone again")["request_id"], "one")
	assertEqual(t, "request_id", logs.message(t, "shared")["request_id"], nil)
	assertEqual(t, "request_id", logs.message(t, "after")["request_id"], nil)
}

func TestPHPLogForgetsARequestWhoseWorkerDied(t *testing.T) {
	logs := captureLogs(t)
	l := newPHPLog()

	l.lines("\x01start dead 10")
	l.finish("dead")
	l.lines("\x01start next 11\nPHP Warning:  mine")

	assertEqual(t, "request_id", logs.message(t, "mine")["request_id"], "next")
}

func TestPHPLogGroupsAStackTrace(t *testing.T) {
	logs := captureLogs(t)

	newPHPLog().read(strings.NewReader("PHP Fatal error:  Uncaught A: first in a.php:1\nStack trace:\n#0 {main}\n\nNext B: second in b.php:2\nStack trace:\n#0 {main}\n  thrown in b.php on line 2\nno newline"))

	records := logs.all(t)
	assertEqual(t, "records", len(records), 2)
	assertEqual(t, "stack", records[0]["stack"], "Stack trace:\n#0 {main}\nNext B: second in b.php:2\nStack trace:\n#0 {main}\n  thrown in b.php on line 2")
	assertEqual(t, "message", records[1]["msg"], "no newline")
}

func TestTraceID(t *testing.T) {
	for header, want := range map[string]string{
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01": "4bf92f3577b34da6a3ce929d0e0e4736",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01": "",
		"00-not-hex-01": "",
		"garbage":       "",
		"":              "",
	} {
		assertEqual(t, header, traceID(header), want)
	}
}

// stuckWriter does not return from Write until it is released, like a pipe nobody reads
type stuckWriter struct {
	release chan struct{}
	logs    logRecords
}

func (w *stuckWriter) Write(data []byte) (int, error) {
	<-w.release
	return w.logs.Write(data)
}

func TestLogWriterDropsInsteadOfBlockingAndSaysHowMany(t *testing.T) {
	out := &stuckWriter{release: make(chan struct{})}
	l := newLogWriter(out, 2)

	finished := make(chan struct{})
	go func() {
		// One record in the writer, 2 in the queue, the rest dropped
		for range 10 {
			time.Sleep(5 * time.Millisecond)
			_, _ = l.Write([]byte(`{"msg":"record"}` + "\n"))
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked on a stuck destination")
	}

	close(out.release)
	l.flush(5 * time.Second)

	records, dropped := 0, 0.0
	for _, record := range out.logs.all(t) {
		if record["msg"] == "logs dropped" {
			assertEqual(t, "level", record["level"], "WARN")
			dropped += record["count"].(float64)
		} else {
			records++
		}
	}
	assertEqual(t, "written and dropped", records+int(dropped), 10)
	assertEqual(t, "written", records, 3)
}

func TestLogWriterFlushWritesWhatIsQueuedInOrder(t *testing.T) {
	var out logRecords
	l := newLogWriter(&out, 1000)

	for i := range 1000 {
		_, _ = l.Write(fmt.Appendf(nil, `{"n":%d}`+"\n", i))
	}
	l.flush(5 * time.Second)

	records := out.all(t)
	assertEqual(t, "records", len(records), 1000)
	for i, record := range records {
		assertEqual(t, "order", record["n"], float64(i))
	}
}

func TestLogWriterFlushDoesNotWaitForAStuckDestination(t *testing.T) {
	out := &stuckWriter{release: make(chan struct{})}
	defer close(out.release)
	l := newLogWriter(out, 2)
	_, _ = l.Write([]byte("{}\n"))

	start := time.Now()
	l.flush(100 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("flush took %s", elapsed)
	}
}

func TestGatewayKeepsServingWhenTheLogDestinationIsStuck(t *testing.T) {
	out := &stuckWriter{release: make(chan struct{})}
	defer close(out.release)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(newLogWriter(out, 4), nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	for range 100 {
		resp, _ := get(t, srv.URL+"/error-log")
		assertEqual(t, "status", resp.StatusCode, http.StatusOK)
	}
}
