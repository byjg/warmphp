package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run real PHP workers. PHPWORKER_PHP sets the command that runs the
// PHP CLI (default "php"); it must pass stdin through, e.g.
// PHPWORKER_PHP="docker run --rm -i --init -v $PWD:$PWD -v /tmp:/tmp -u 1000:1000 byjg/php:8.5-cli php"
func phpCommand(t *testing.T) []string {
	command := strings.Fields(os.Getenv("PHPWORKER_PHP"))
	if len(command) == 0 {
		command = []string{"php"}
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		t.Skipf("PHP command %q not found", command[0])
	}
	return command
}

func startGateway(t *testing.T, mode, entry string, configure func(*config)) *httptest.Server {
	t.Helper()
	workerScript, _ := filepath.Abs("php/worker.php")
	entry, _ = filepath.Abs(entry)
	cfg := config{
		php:          phpCommand(t),
		workerScript: workerScript,
		mode:         mode,
		entry:        entry,
		workers:      1,
		timeout:      10 * time.Second,
		bootTimeout:  60 * time.Second,
		maxBody:      1 << 20,
		uploadDir:    t.TempDir(),
	}
	if configure != nil {
		configure(&cfg)
	}

	g, err := newGateway(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(func() {
		srv.Close()
		g.Close()
	})
	return srv
}

func send(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return send(t, req)
}

type counter struct {
	Boot  string `json:"boot"`
	Count int    `json:"count"`
}

func getCount(t *testing.T, srv *httptest.Server) counter {
	t.Helper()
	resp, body := get(t, srv.URL+"/count")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/count: status %d, body %q", resp.StatusCode, body)
	}
	var c counter
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("/count: %v in %q", err, body)
	}
	return c
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%v in %q", err, body)
	}
	return v
}

func assertEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v, want %#v", what, got, want)
	}
}

func TestBootsOnceAndKeepsStateBetweenRequests(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	first := getCount(t, srv)
	for i := 2; i <= 20; i++ {
		c := getCount(t, srv)
		assertEqual(t, "boot id", c.Boot, first.Boot)
		assertEqual(t, "count", c.Count, i)
	}
}

func TestRequestVariables(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	t.Run("query, cookies and headers", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/vars?a=1&list[]=x&list[]=y", nil)
		req.Header.Set("Cookie", "session=abc%20def; theme=dark")
		req.Header.Set("X-Custom", "hello")
		_, body := send(t, req)
		v := decode(t, body)

		assertEqual(t, "get", v["get"], map[string]any{"a": "1", "list": []any{"x", "y"}})
		assertEqual(t, "cookie", v["cookie"], map[string]any{"session": "abc def", "theme": "dark"})
		server := v["server"].(map[string]any)
		assertEqual(t, "REQUEST_METHOD", server["REQUEST_METHOD"], "GET")
		assertEqual(t, "REQUEST_URI", server["REQUEST_URI"], "/vars?a=1&list[]=x&list[]=y")
		assertEqual(t, "HTTP_X_CUSTOM", server["HTTP_X_CUSTOM"], "hello")
		assertEqual(t, "SCRIPT_NAME", server["SCRIPT_NAME"], "/app.php")
		if path, _ := server["PATH"].(string); path == "" {
			t.Errorf("environment variable PATH missing from $_SERVER")
		}
	})

	t.Run("urlencoded form", func(t *testing.T) {
		form := url.Values{"name": {"John"}, "user[email]": {"j@example.com"}}
		resp, err := http.PostForm(srv.URL+"/vars?name=query", form)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		v := decode(t, string(body))

		assertEqual(t, "post", v["post"], map[string]any{"name": "John", "user": map[string]any{"email": "j@example.com"}})
		assertEqual(t, "request: POST overrides GET", v["request"].(map[string]any)["name"], "John")
		assertEqual(t, "input", v["input"], form.Encode())
	})

	t.Run("raw JSON body", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/vars", strings.NewReader(`{"id":7}`))
		req.Header.Set("Content-Type", "application/json")
		_, body := send(t, req)
		v := decode(t, body)

		assertEqual(t, "input", v["input"], `{"id":7}`)
		assertEqual(t, "post", v["post"], []any{})
		assertEqual(t, "CONTENT_TYPE", v["server"].(map[string]any)["CONTENT_TYPE"], "application/json")
	})

	t.Run("multipart with files", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("title", "report")
		_ = mw.WriteField("tags[]", "a")
		_ = mw.WriteField("tags[]", "b")
		for name, content := range map[string]string{"avatar": "PNGDATA"} {
			fw, _ := mw.CreateFormFile(name, "me.png")
			_, _ = fw.Write([]byte(content))
		}
		for _, f := range []struct{ name, content string }{{"one.txt", "first"}, {"two.txt", "second!"}} {
			fw, _ := mw.CreateFormFile("docs[]", f.name)
			_, _ = fw.Write([]byte(f.content))
		}
		_, _ = mw.CreateFormFile("empty", "")
		mw.Close()

		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/vars", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		_, body := send(t, req)
		v := decode(t, body)

		assertEqual(t, "post", v["post"], map[string]any{"title": "report", "tags": []any{"a", "b"}})
		files := v["files"].(map[string]any)
		avatar := files["avatar"].(map[string]any)
		assertEqual(t, "avatar name", avatar["name"], "me.png")
		assertEqual(t, "avatar size", avatar["size"], float64(7))
		assertEqual(t, "avatar error", avatar["error"], float64(0))
		docs := files["docs"].(map[string]any)
		assertEqual(t, "docs names", docs["name"], []any{"one.txt", "two.txt"})
		assertEqual(t, "docs sizes", docs["size"], []any{float64(5), float64(7)})
		assertEqual(t, "empty upload error", files["empty"].(map[string]any)["error"], float64(4))
		assertEqual(t, "contents", v["contents"], map[string]any{
			"avatar": []any{"PNGDATA"},
			"docs":   []any{"first", "second!"},
			"empty":  []any{nil},
		})
	})

	t.Run("nothing leaks into the next request", func(t *testing.T) {
		_, body := get(t, srv.URL+"/vars")
		v := decode(t, body)
		for _, key := range []string{"get", "post", "cookie", "request", "files"} {
			assertEqual(t, key, v[key], []any{})
		}
		assertEqual(t, "input", v["input"], "")
	})
}

func TestUploadedFilesAreRemovedAfterTheResponse(t *testing.T) {
	uploadDir := t.TempDir()
	srv := startGateway(t, "callable", "testdata/app.php", func(cfg *config) { cfg.uploadDir = uploadDir })

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.txt")
	_, _ = fw.Write([]byte("data"))
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/vars", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	send(t, req)

	entries, _ := os.ReadDir(uploadDir)
	assertEqual(t, "files left in upload dir", len(entries), 0)
}

func TestResponseStatusAndHeaders(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	resp, body := get(t, srv.URL+"/headers")
	assertEqual(t, "status", resp.StatusCode, http.StatusCreated)
	assertEqual(t, "body", body, "{}")
	assertEqual(t, "Content-Type", resp.Header.Get("Content-Type"), "application/json")
	assertEqual(t, "X-Test replaced", resp.Header.Values("X-Test"), []string{"second"})
	assertEqual(t, "Set-Cookie appended", resp.Header.Values("Set-Cookie"), []string{"a=1", "b=2"})

	resp, _ = get(t, srv.URL+"/redirect")
	assertEqual(t, "redirect status", resp.StatusCode, http.StatusFound)
	assertEqual(t, "Location", resp.Header.Get("Location"), "/count")

	resp, _ = get(t, srv.URL+"/count")
	assertEqual(t, "status resets to 200", resp.StatusCode, http.StatusOK)
	assertEqual(t, "headers reset", resp.Header.Get("X-Test"), "")

	_, body = get(t, srv.URL+"/nested-buffer")
	assertEqual(t, "buffers left open are part of the body", body, "outer inner")
}

func TestDefaultContentType(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	resp, _ := get(t, srv.URL+"/count")
	assertEqual(t, "default Content-Type", resp.Header.Values("Content-Type"), []string{"text/html; charset=UTF-8"})
	resp, _ = get(t, srv.URL+"/headers")
	assertEqual(t, "application Content-Type", resp.Header.Values("Content-Type"), []string{"application/json"})
}

func TestHeaderFunctionsAreOverridden(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	resp, body := get(t, srv.URL+"/header-functions")
	v := decode(t, body)
	assertEqual(t, "headers_list", v["list"], []any{"X-Keep: 1", "X-Multi: 1", "X-Multi: 2", "X-Teapot: yes", "X-Namespaced: yes"})
	assertEqual(t, "headers_sent", v["sent"], false)
	assertEqual(t, "http_response_code after header(..., 418)", v["previous"], float64(418))
	assertEqual(t, "status", resp.StatusCode, http.StatusTeapot)
	assertEqual(t, "X-Multi", resp.Header.Values("X-Multi"), []string{"1", "2"})
	assertEqual(t, "removed header", resp.Header.Get("X-Drop"), "")
	assertEqual(t, "header with a new line is rejected", resp.Header.Get("X-Injected")+resp.Header.Get("X-Evil"), "")
	assertEqual(t, "header() from namespaced code", resp.Header.Get("X-Namespaced"), "yes")
}

func TestSetcookieMatchesPHP(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	resp, body := get(t, srv.URL+"/cookies")
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) != 5 {
		t.Fatalf("expected 5 cookies, got %q", cookies)
	}
	// Expected lines are what PHP 8.5's built-in web server sends for the same calls
	assertEqual(t, "setcookie urlencodes", cookies[0], "plain=a%20b%2Bc%2F%C3%A9")
	assertEqual(t, "empty value deletes", cookies[1], "gone=deleted; expires=Thu, 01 Jan 1970 00:00:01 GMT; Max-Age=0")
	assertCookie(t, cookies[2], "full=v; expires=Wed, 18 May 2033 03:33:20 GMT; Max-Age=", "; path=/p; domain=ex.com; secure; HttpOnly; SameSite=Lax; Partitioned")
	assertCookie(t, cookies[3], "old=v; expires=Wed, 18 May 2033 03:33:20 GMT; Max-Age=", "; path=/; HttpOnly")
	assertEqual(t, "setrawcookie", cookies[4], "raw=a%20b")
	assertEqual(t, "invalid name", body, `setcookie(): Argument #1 ($name) cannot contain "=", ",", ";", " ", "\t", "\r", "\n", "\013", or "\014"`)
}

// assertCookie checks a cookie whose Max-Age depends on the current time.
func assertCookie(t *testing.T, cookie, prefix, suffix string) {
	t.Helper()
	maxAge, ok := strings.CutPrefix(cookie, prefix)
	if ok {
		maxAge, ok = strings.CutSuffix(maxAge, suffix)
	}
	if !ok || strings.Trim(maxAge, "0123456789") != "" {
		t.Errorf("cookie %q does not match %q<Max-Age>%q", cookie, prefix, suffix)
	}
}

func TestUploadedFileFunctions(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)
	target := t.TempDir()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "photo.jpg")
	_, _ = fw.Write([]byte("JPEGDATA"))
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload?to="+url.QueryEscape(target), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	_, body := send(t, req)

	assertEqual(t, "results", decode(t, body), map[string]any{
		"is_uploaded":            true,
		"other_file_is_uploaded": false,
		"move_other_file":        false,
		"moved":                  true,
		"moved_twice":            false,
	})
	content, err := os.ReadFile(filepath.Join(target, "photo.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "moved content", string(content), "JPEGDATA")
	if _, err := os.Stat(filepath.Join(target, "stolen")); !os.IsNotExist(err) {
		t.Errorf("move_uploaded_file() moved a file that was not uploaded")
	}
}

type session struct {
	Visits int    `json:"visits"`
	ID     string `json:"id"`
}

func getSession(t *testing.T, url, cookie string) (session, []string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if cookie != "" {
		req.Header.Set("Cookie", "PHPSESSID="+cookie)
	}
	resp, body := send(t, req)
	var s session
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("%v in %q", err, body)
	}
	return s, resp.Header.Values("Set-Cookie"), resp.Header
}

func TestSessions(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	first, cookies, headers := getSession(t, srv.URL+"/session", "")
	assertEqual(t, "new session visits", first.Visits, 1)
	assertEqual(t, "session cookie", cookies, []string{"PHPSESSID=" + first.ID + "; path=/"})
	assertEqual(t, "Cache-Control", headers.Get("Cache-Control"), "no-store, no-cache, must-revalidate")
	assertEqual(t, "Expires", headers.Get("Expires"), "Thu, 19 Nov 1981 08:52:00 GMT")
	assertEqual(t, "Pragma", headers.Get("Pragma"), "no-cache")

	again, cookies, _ := getSession(t, srv.URL+"/session", first.ID)
	assertEqual(t, "same session", again, session{Visits: 2, ID: first.ID})
	assertEqual(t, "no cookie for an existing session", len(cookies), 0)

	resp, body := get(t, srv.URL+"/no-session")
	assertEqual(t, "no session leaks into the next request", decode(t, body), map[string]any{"session_set": false, "status": float64(1)})
	assertEqual(t, "no cookie without session_start()", len(resp.Header.Values("Set-Cookie")), 0)
	assertEqual(t, "no cache headers without session_start()", resp.Header.Get("Pragma"), "")

	regenerated, cookies, _ := getSession(t, srv.URL+"/session?regenerate=1", first.ID)
	if regenerated.ID == first.ID || regenerated.Visits != 3 {
		t.Errorf("expected a regenerated id keeping the data, got %+v", regenerated)
	}
	assertEqual(t, "cookie for the regenerated id", cookies, []string{"PHPSESSID=" + regenerated.ID + "; path=/"})

	closed, cookies, _ := getSession(t, srv.URL+"/session?close=1", "")
	assertEqual(t, "cookie when the application closes the session itself", cookies, []string{"PHPSESSID=" + closed.ID + "; path=/"})
	reopened, _, _ := getSession(t, srv.URL+"/session", closed.ID)
	assertEqual(t, "data written by session_write_close()", reopened.Visits, 2)

	// The session cookie comes where session_start() / session_regenerate_id() would send it
	own, cookies, _ := getSession(t, srv.URL+"/session?own-cookie=1", "")
	assertEqual(t, "session cookie, then the application's own", cookies, []string{
		"PHPSESSID=" + own.ID + "; path=/",
		"PHPSESSID=" + own.ID + "; path=/; SameSite=lax",
	})
	renewed, cookies, _ := getSession(t, srv.URL+"/session?renew=1", own.ID)
	if len(cookies) != 2 || !strings.HasPrefix(cookies[0], "PHPSESSID=deleted;") || cookies[1] != "PHPSESSID="+renewed.ID+"; path=/" {
		t.Errorf("expected the deleted cookie, then the regenerated id %s; got %q", renewed.ID, cookies)
	}
}

func TestOutputOutsideTheBuffersIsTheBody(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	resp, body := get(t, srv.URL+"/close-buffers")
	assertEqual(t, "body after closing every buffer", body, "abc")
	assertEqual(t, "header set after output", resp.Header.Get("X-After-Output"), "yes")

	_, body = get(t, srv.URL+"/large")
	var want strings.Builder
	for i := range 1000 {
		want.WriteString(fmt.Sprintf("%-10000s\n", strconv.Itoa(i)))
	}
	if body != strings.ReplaceAll(want.String(), " ", ".") {
		t.Errorf("large body: got %d bytes, want %d", len(body), want.Len())
	}

	// Bytes that look like the protocol must reach the client untouched
	data := []byte("\x00\xff\r\n--phpworker-\x00\x00\x00\x10{\"status\":500}")
	_, body = get(t, srv.URL+"/binary?data="+url.QueryEscape(base64.StdEncoding.EncodeToString(data)))
	assertEqual(t, "binary body", body, string(data))

	before := getCount(t, srv)
	assertEqual(t, "the same worker served every request", before.Count, 4)

	// Output that reaches PHP's output layer marks the headers as sent for the whole process
	// (sessions stop working), so the body is delivered and the worker replaced
	_, body = get(t, srv.URL+"/level-zero")
	assertEqual(t, "output at level zero", body, "level zero")
	after := getCount(t, srv)
	if after.Boot == before.Boot || after.Count != 1 {
		t.Errorf("expected a new worker after output at level zero, got %+v (before %+v)", after, before)
	}
}

func TestUncaughtExceptionKeepsTheWorker(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)
	before := getCount(t, srv)

	resp, body := get(t, srv.URL+"/exception")
	assertEqual(t, "status", resp.StatusCode, http.StatusInternalServerError)
	assertEqual(t, "body", body, "partial")

	after := getCount(t, srv)
	assertEqual(t, "same worker", after.Boot, before.Boot)
}

func TestExitAndFatalErrorAnswerAndReplaceTheWorker(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", nil)

	before := getCount(t, srv)
	resp, body := get(t, srv.URL+"/exit")
	assertEqual(t, "exit status", resp.StatusCode, http.StatusOK)
	assertEqual(t, "exit body", body, "bye")

	afterExit := getCount(t, srv)
	if afterExit.Boot == before.Boot || afterExit.Count != 1 {
		t.Errorf("expected a new worker after exit, got %+v (before %+v)", afterExit, before)
	}

	resp, _ = get(t, srv.URL+"/fatal")
	assertEqual(t, "fatal status", resp.StatusCode, http.StatusInternalServerError)

	afterFatal := getCount(t, srv)
	if afterFatal.Boot == afterExit.Boot || afterFatal.Count != 1 {
		t.Errorf("expected a new worker after a fatal error, got %+v (before %+v)", afterFatal, afterExit)
	}

	resp, body = get(t, srv.URL+"/memory-full")
	assertEqual(t, "memory exhausted status", resp.StatusCode, http.StatusInternalServerError)
	assertEqual(t, "memory exhausted body", body, strings.Repeat("p", 2<<20))
}

func TestTimeoutKillsAndReplacesTheWorker(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", func(cfg *config) { cfg.timeout = time.Second })

	resp, _ := get(t, srv.URL+"/sleep")
	assertEqual(t, "status", resp.StatusCode, http.StatusGatewayTimeout)

	// The replacement boots in the background; wait for it
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, body := get(t, srv.URL+"/count")
		if resp.StatusCode == http.StatusOK {
			assertEqual(t, "new worker count", decode(t, body)["count"], float64(1))
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no worker came back after the timeout: %d %q", resp.StatusCode, body)
		}
	}
}

func TestMaxRequestsRecyclesTheWorker(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", func(cfg *config) {
		cfg.maxRequests = 3
		cfg.timeout = 60 * time.Second
	})

	var counts []int
	for range 5 {
		counts = append(counts, getCount(t, srv).Count)
	}
	assertEqual(t, "counts", counts, []int{1, 2, 3, 1, 2})
}

func TestConcurrentRequestsUseThePool(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", func(cfg *config) { cfg.workers = 3 })

	var mu sync.Mutex
	boots := map[string]int{}
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			c := getCount(t, srv)
			mu.Lock()
			boots[c.Boot]++
			mu.Unlock()
		})
	}
	wg.Wait()

	total := 0
	for _, n := range boots {
		total += n
	}
	assertEqual(t, "requests served", total, 30)
	if len(boots) > 3 {
		t.Errorf("expected at most 3 workers, got %d", len(boots))
	}
}

func TestScriptModeIncludesTheEntryOnEveryRequest(t *testing.T) {
	srv := startGateway(t, "script", "testdata/script.php", nil)

	for i, name := range []string{"ana", "bob", "carl"} {
		_, body := get(t, srv.URL+"/?name="+name)
		v := decode(t, body)
		assertEqual(t, "name", v["name"], name)
		assertEqual(t, "globals persist in the process", v["hits"], float64(i+1))
		assertEqual(t, "locals do not leak", v["leaked"], false)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	srv := startGateway(t, "callable", "testdata/app.php", func(cfg *config) { cfg.maxBody = 10 })

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/vars", strings.NewReader(strings.Repeat("x", 11)))
	resp, _ := send(t, req)
	assertEqual(t, "status", resp.StatusCode, http.StatusRequestEntityTooLarge)
}
