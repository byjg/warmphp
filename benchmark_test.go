//go:build compat

package main

// php -S against phpworker-gateway on the builds of gluo-framework-comparison: every framework
// serves the same user journey (see docs/benchmark.md), with OPcache on and off.
//
//   go test -tags compat -run TestBenchmark -v -timeout 3h                   # BENCH_JOURNEYS=3000
//   BENCH_JOURNEYS=10 go test -tags compat -run TestBenchmark -v             # smoke run
//
// BENCH_JOURNEYS  journeys per run (default 3000); BENCH_CLIENTS  concurrent clients and PHP
// processes on each side (default 4); BENCH_ONLY  runs the targets whose name contains it.
// Results: benchmark/results.tsv.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// benchTarget is a build of the comparison and how php -S serves it
type benchTarget struct {
	framework
	journey   func(b *browser, fw framework) ([]time.Duration, error)
	phpServer []string // php -S arguments after the address, run in phpServerDir
	serverDir string   // relative to the project; the project when empty
	noPhpS    bool     // the php -S row of another target covers this one
	suite     string   // "fullstack" or "api"; empty for Gluo, which is in both
}

// The steps of a journey: the login page, the login, the product list
var benchSteps = []string{"page", "login", "list"}

func timed(steps *[]time.Duration, request func() error) error {
	start := time.Now()
	err := request()
	*steps = append(*steps, time.Since(start))
	return err
}

// journeyForms: Symfony, CakePHP and CodeIgniter, server-rendered forms with CSRF
func journeyForms(b *browser, fw framework) ([]time.Duration, error) {
	var steps []time.Duration
	var login page
	if err := timed(&steps, func() (err error) {
		login, err = b.try(http.MethodGet, fw.loginPath, nil, nil)
		return err
	}); err != nil {
		return steps, err
	}
	i := slices.IndexFunc(forms(login.body), hasField(fw.passwordField))
	if i < 0 {
		return steps, fmt.Errorf("no login form (%d %s)", login.status, login.path)
	}
	form := forms(login.body)[i]
	form.fields.Set(fw.userField, adminEmail)
	form.fields.Set(fw.passwordField, adminPassword)
	action := cmpOr(form.action, fw.loginPath)
	if u, err := url.Parse(action); err == nil && u.IsAbs() {
		action = u.RequestURI()
	}

	if err := timed(&steps, func() error {
		p, err := b.try(cmpOr(form.method, http.MethodPost), action, strings.NewReader(form.fields.Encode()),
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if err == nil && (p.status != http.StatusOK || p.path == fw.loginPath) {
			err = fmt.Errorf("login failed: %d %s", p.status, p.path)
		}
		return err
	}); err != nil {
		return steps, err
	}

	err := timed(&steps, func() error {
		p, err := b.try(http.MethodGet, fw.listPath, nil, nil)
		// Every product row links to its edit page
		if err == nil && (p.status != http.StatusOK || p.path != fw.listPath || !strings.Contains(p.body, "/edit")) {
			err = fmt.Errorf("no product list: %d %s", p.status, p.path)
		}
		return err
	})
	return steps, err
}

// journeyInertia: Laravel's React screens, which log in and navigate with JSON requests
func journeyInertia(b *browser, fw framework) ([]time.Duration, error) {
	var steps []time.Duration
	var version string
	if err := timed(&steps, func() error {
		p, err := b.try(http.MethodGet, fw.loginPath, nil, nil)
		if err != nil {
			return err
		}
		m := dataPageRe.FindStringSubmatch(p.body)
		if m == nil {
			return fmt.Errorf("no Inertia page: %d %s", p.status, p.path)
		}
		var ip inertiaPage
		err = json.Unmarshal([]byte(html.UnescapeString(m[1]+m[2])), &ip)
		version = ip.Version
		return err
	}); err != nil {
		return steps, err
	}

	inertia := func(method, path string, body any) (page, inertiaPage, error) {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		xsrf, _ := url.QueryUnescape(b.cookie("XSRF-TOKEN"))
		p, err := b.try(method, path, strings.NewReader(string(data)), map[string]string{
			"Content-Type": "application/json", "Accept": "text/html, application/xhtml+xml",
			"X-Requested-With": "XMLHttpRequest", "X-Inertia": "true", "X-Inertia-Version": version, "X-XSRF-TOKEN": xsrf,
		})
		var ip inertiaPage
		if err == nil {
			_ = json.Unmarshal([]byte(p.body), &ip)
		}
		return p, ip, err
	}

	if err := timed(&steps, func() error {
		p, _, err := inertia(http.MethodPost, fw.loginPath, map[string]any{"email": adminEmail, "password": adminPassword})
		if err == nil && (p.status != http.StatusOK || p.path == fw.loginPath) {
			err = fmt.Errorf("login failed: %d %s", p.status, p.path)
		}
		return err
	}); err != nil {
		return steps, err
	}

	err := timed(&steps, func() error {
		p, ip, err := inertia(http.MethodGet, fw.listPath, nil)
		if products, _ := ip.Props["products"].([]any); err == nil && (p.status != http.StatusOK || len(products) == 0) {
			err = fmt.Errorf("no product list: %d %s", p.status, p.path)
		}
		return err
	})
	return steps, err
}

// journeyAPI: Gluo. Its login page is the React SPA, a static file: no PHP for that step
func journeyAPI(b *browser, fw framework) ([]time.Duration, error) {
	steps := []time.Duration{0}
	var token string
	if err := timed(&steps, func() error {
		p, data, err := b.tryAPI(http.MethodPost, fw.loginPath, "", map[string]string{"username": adminEmail, "password": adminPassword})
		token, _ = data["token"].(string)
		if err == nil && (p.status != http.StatusOK || token == "") {
			err = fmt.Errorf("login failed: %d %.100s", p.status, p.body)
		}
		return err
	}); err != nil {
		return steps, err
	}

	err := timed(&steps, func() error {
		p, err := b.try(http.MethodGet, fw.listPath, nil, map[string]string{"Accept": "application/json", "Authorization": "Bearer " + token})
		var products []any
		if err == nil {
			_ = json.Unmarshal([]byte(p.body), &products)
		}
		if err == nil && (p.status != http.StatusOK || len(products) == 0) {
			err = fmt.Errorf("no product list: %d %.100s", p.status, p.body)
		}
		return err
	})
	return steps, err
}

func (b *browser) tryAPI(method, path, token string, body any) (page, map[string]any, error) {
	data, _ := json.Marshal(body)
	headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	p, err := b.try(method, path, strings.NewReader(string(data)), headers)
	var decoded map[string]any
	if err == nil {
		_ = json.Unmarshal([]byte(p.body), &decoded)
	}
	return p, decoded, err
}

type benchResult struct {
	journeys, errors int
	elapsed          time.Duration
	totals           []time.Duration
	steps            [][]time.Duration
	firstError       string
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(float64(len(sorted))*p))]
}

func mean(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var sum time.Duration
	for _, v := range values {
		sum += v
	}
	return sum / time.Duration(len(values))
}

// runJourneys runs n journeys with c clients, each journey with a new browser (cookies, connections)
func runJourneys(t *testing.T, target benchTarget, addr string, n, c int) benchResult {
	result := benchResult{journeys: n, steps: make([][]time.Duration, len(benchSteps))}
	var mu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup
	start := time.Now()
	for range c {
		wg.Go(func() {
			for range jobs {
				b := newBrowser(t, target.baseURL, addr)
				begin := time.Now()
				steps, err := target.journey(b, target.framework)
				total := time.Since(begin)
				b.http.CloseIdleConnections()
				mu.Lock()
				if err != nil {
					result.errors++
					if result.firstError == "" {
						result.firstError = err.Error()
					}
				} else {
					result.totals = append(result.totals, total)
					for i, d := range steps {
						result.steps[i] = append(result.steps[i], d)
					}
				}
				mu.Unlock()
			}
		})
	}
	for i := range n {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	result.elapsed = time.Since(start)
	slices.Sort(result.totals)
	return result
}

// benchPHP is how PHP runs on both sides: the same image, user, mounts and settings
func benchPHP(root string, target benchTarget, opcache string, docker ...string) []string {
	here, _ := filepath.Abs(".")
	image := cmpOr(os.Getenv("PHPWORKER_IMAGE"), "byjg/php:8.5-cli-load")
	cmd := append([]string{"docker", "run", "--rm", "--network", "host", "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", root + ":" + root, "-v", here + ":" + here, "-v", "/tmp:/tmp", "-e", "PROJECT_ROOT=" + root}, docker...)
	for _, env := range target.env {
		cmd = append(cmd, "-e", env)
	}
	// php -S ignores opcache.enable_cli (its SAPI is cli-server): both settings, on both sides
	return append(cmd, image, "php", "-d", "opcache.enable="+opcache, "-d", "opcache.enable_cli="+opcache)
}

func waitHTTP(t *testing.T, addr, host string) {
	t.Helper()
	// Any answer means the server is up; a redirect (CodeIgniter's /) goes to the configured host
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for range 240 {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
		req.Host = host
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("nothing answers on %s", addr)
}

// startPhpS serves the target with the built-in server and C processes (PHP_CLI_SERVER_WORKERS)
func startPhpS(t *testing.T, target benchTarget, root, opcache string, c int) string {
	addr := "127.0.0.1:18811"
	name := "phpworker-bench-" + strconv.Itoa(os.Getpid())
	args := benchPHP(root, target, opcache, "-d", "--name", name, "-w", filepath.Join(root, target.serverDir),
		"-e", "PHP_CLI_SERVER_WORKERS="+strconv.Itoa(c))
	args = append(args, "-d", "variables_order=EGPCS", "-S", addr)
	args = append(args, target.phpServer...)
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("php -S: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "stop", name).Run() })
	u, _ := url.Parse(target.baseURL)
	waitHTTP(t, addr, u.Host)
	return addr
}

// startWorkers serves the target with the gateway, C workers and the boot file of compat/
func startWorkers(t *testing.T, target benchTarget, root, opcache string, c int) string {
	here, _ := filepath.Abs(".")
	g, err := newGateway(config{
		php:          benchPHP(root, target, opcache, "-i", "--init"),
		workerScript: filepath.Join(here, "php/worker.php"),
		mode:         "callable",
		entry:        filepath.Join(here, "compat", target.boot),
		workers:      c,
		maxRequests:  target.maxRequests,
		timeout:      60 * time.Second,
		bootTimeout:  120 * time.Second,
		maxBody:      1 << 20,
		uploadDir:    os.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(func() {
		srv.Close()
		g.Close()
	})
	return srv.Listener.Addr().String()
}

func TestBenchmark(t *testing.T) {
	journeys, _ := strconv.Atoi(cmpOr(os.Getenv("BENCH_JOURNEYS"), "3000"))
	clients, _ := strconv.Atoi(cmpOr(os.Getenv("BENCH_CLIENTS"), "4"))
	laravelRouter := "vendor/laravel/framework/src/Illuminate/Foundation/resources/server.php"
	laravelEnv := []string{"DB_HOST=127.0.0.1", "DB_PORT=3307", "CACHE_STORE=array"}
	// The API builds run with the settings of their full-stack siblings (their .env has the database)
	apiLaravelEnv := []string{"CACHE_STORE=array"}

	targets := []benchTarget{
		{framework: framework{name: "symfony", project: "frameworks/fullstack/symfony-official", boot: "symfony.php", baseURL: "http://localhost:8083",
			env: []string{"APP_ENV=prod", "APP_DEBUG=0"}, loginPath: "/login", listPath: "/product",
			userField: "_username", passwordField: "_password"},
			journey: journeyForms, phpServer: []string{"-t", "public", "public/index.php"}, suite: "fullstack"},
		{framework: framework{name: "cakephp", project: "frameworks/fullstack/cakephp", boot: "cakephp.php", baseURL: "http://localhost:8084",
			env: []string{"DEBUG=false", "APP_FULL_BASE_URL=http://localhost:8084"}, loginPath: "/users/login", listPath: "/products",
			userField: "email", passwordField: "password"},
			journey: journeyForms, phpServer: []string{"-t", "webroot", "webroot/index.php"}, suite: "fullstack"},
		{framework: framework{name: "codeigniter", project: "frameworks/fullstack/codeigniter", boot: "codeigniter.php", baseURL: "http://localhost:8085",
			loginPath: "/login", listPath: "/products", userField: "email", passwordField: "password"},
			journey: journeyForms, phpServer: []string{"-t", "public", "vendor/codeigniter4/framework/system/rewrite.php"}, suite: "fullstack"},
		{framework: framework{name: "laravel-fresh", project: "frameworks/fullstack/laravel", boot: "laravel-fresh.php", baseURL: "http://localhost:8081",
			env: laravelEnv, loginPath: "/login", listPath: "/products", maxRequests: 30},
			journey: journeyInertia, serverDir: "public", phpServer: []string{"-t", ".", "../" + laravelRouter}, suite: "fullstack"},
		{framework: framework{name: "laravel-shared", project: "frameworks/fullstack/laravel", boot: "laravel-shared.php", baseURL: "http://localhost:8081",
			env: laravelEnv, loginPath: "/login", listPath: "/products"},
			journey: journeyInertia, noPhpS: true, suite: "fullstack"},
		{framework: framework{name: "gluo", project: "frameworks/gluo", boot: "gluo.php", baseURL: "http://localhost:8080",
			env: []string{"APP_ENV=dev"}, loginPath: "/login", listPath: "/product", requires: gluoApplication},
			journey: journeyAPI, phpServer: []string{"-t", "public", "public/app.php"}},

		// The same frameworks as JSON APIs in front of Gluo's SPA: the journey is Gluo's
		{framework: framework{name: "api-symfony", project: "frameworks/api/symfony", boot: "symfony.php", baseURL: "http://localhost:9082",
			env: []string{"APP_ENV=prod", "APP_DEBUG=0"}, loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, phpServer: []string{"-t", "public", "public/index.php"}, suite: "api"},
		{framework: framework{name: "api-cakephp", project: "frameworks/api/cakephp", boot: "cakephp.php", baseURL: "http://localhost:9083",
			env: []string{"DEBUG=false", "APP_FULL_BASE_URL=http://localhost:9083"}, loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, phpServer: []string{"-t", "webroot", "webroot/index.php"}, suite: "api"},
		{framework: framework{name: "api-codeigniter", project: "frameworks/api/codeigniter", boot: "codeigniter.php", baseURL: "http://localhost:9084",
			loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, phpServer: []string{"-t", "public", "vendor/codeigniter4/framework/system/rewrite.php"}, suite: "api"},
		{framework: framework{name: "api-laravel-fresh", project: "frameworks/api/laravel", boot: "laravel-fresh.php", baseURL: "http://localhost:9081",
			env: apiLaravelEnv, loginPath: "/login", listPath: "/product", maxRequests: 30},
			journey: journeyAPI, serverDir: "public", phpServer: []string{"-t", ".", "../" + laravelRouter}, suite: "api"},
		{framework: framework{name: "api-laravel-shared", project: "frameworks/api/laravel", boot: "laravel-shared.php", baseURL: "http://localhost:9081",
			env: apiLaravelEnv, loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, noPhpS: true, suite: "api"},
	}

	out := filepath.Join("benchmark", "results.tsv")
	_ = os.MkdirAll("benchmark", 0o755)
	if _, err := os.Stat(out); errors.Is(err, os.ErrNotExist) || os.Getenv("BENCH_ONLY") == "" {
		_ = os.WriteFile(out, []byte("suite\ttarget\tserver\topcache\tjourneys\terrors\tjourneys/s\tp50\tp99\tpage\tlogin\tlist\tfirst error\n"), 0o644)
	}
	write := func(line string) {
		f, err := os.OpenFile(out, os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(line + "\n")
			f.Close()
		}
	}

	for _, target := range targets {
		if only := os.Getenv("BENCH_ONLY"); only != "" && !strings.Contains(target.name, only) {
			continue
		}
		if suite := os.Getenv("BENCH_SUITE"); suite != "" && target.suite != "" && target.suite != suite {
			continue
		}
		root := filepath.Join(compatDir(t), target.project)
		if target.requires != "" {
			if _, err := os.Stat(filepath.Join(root, target.requires)); err != nil {
				t.Errorf("%s has no %s", root, target.requires)
				continue
			}
		}
		for _, opcache := range []string{"1", "0"} {
			label := map[string]string{"1": "on", "0": "off"}[opcache]
			servers := []string{"php -S", "worker"}
			if target.noPhpS {
				servers = []string{"worker"}
			}
			for _, server := range servers {
				t.Run(fmt.Sprintf("%s/%s/opcache-%s", target.name, strings.ReplaceAll(server, " ", ""), label), func(t *testing.T) {
					start := startWorkers
					if server == "php -S" {
						start = startPhpS
					}
					addr := start(t, target, root, opcache, clients)
					runJourneys(t, target, addr, max(clients, journeys/20), clients) // warm-up
					r := runJourneys(t, target, addr, journeys, clients)

					stepMeans := make([]string, len(benchSteps))
					for i := range benchSteps {
						stepMeans[i] = mean(r.steps[i]).Round(10 * time.Microsecond).String()
					}
					// php -S has one Laravel row per suite: an application per request
					name := target.name
					if server == "php -S" {
						name = strings.TrimSuffix(name, "-fresh")
					}
					line := strings.Join([]string{cmpOr(target.suite, "both"), name, server, label, strconv.Itoa(r.journeys), strconv.Itoa(r.errors),
						fmt.Sprintf("%.1f", float64(r.journeys-r.errors)/r.elapsed.Seconds()),
						percentile(r.totals, 0.50).Round(10 * time.Microsecond).String(),
						percentile(r.totals, 0.99).Round(10 * time.Microsecond).String(),
						stepMeans[0], stepMeans[1], stepMeans[2], strings.Join(strings.Fields(r.firstError), " ")}, "\t")
					write(line)
					t.Log(line)
					if r.errors > 0 {
						t.Errorf("%d of %d journeys failed, first: %s", r.errors, r.journeys, r.firstError)
					}
				})
			}
		}
	}
}
