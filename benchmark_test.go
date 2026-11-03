//go:build compat

package main

// PHP-FPM, warmphp (with and without -prefork) and FrankenPHP (classic and worker mode) on the builds
// of gluo-framework-comparison: every framework serves the same crud journey (see
// docs/benchmark.md), with OPcache on and off.
//
//   go test -tags compat -run TestBenchmark -v -timeout 3h                   # BENCH_JOURNEYS=3000
//   BENCH_JOURNEYS=10 go test -tags compat -run TestBenchmark -v             # smoke run
//
// BENCH_JOURNEYS  journeys per run (default 3000); BENCH_CLIENTS  concurrent clients and PHP
// processes on each server (default 4); BENCH_ONLY  runs the targets whose name contains it;
// BENCH_SUITE  fullstack or api; BENCH_SERVERS  the servers to run, comma separated (default
// fpm,frankenphp-classic,worker,prefork,frankenphp); BENCH_PHP_INI  PHP settings for every server, space separated
// (default xdebug.mode=off: the PHP image loads Xdebug); BENCH_IMAGE  the PHP image (default
// byjg/php:8.5-fpm-nginx); BENCH_GATEWAY  a static gateway binary (default: go build);
// COMPAT_DIR  the comparison repository (default ../gluo-framework-comparison).
// Results: benchmark/results.tsv.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"math/rand/v2"
	"net/http"
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

// benchTarget is a build of the comparison and how nginx serves it
type benchTarget struct {
	framework
	journey func(b *browser, target benchTarget) ([]time.Duration, error)
	docRoot string // the public folder, relative to the project
	front   string // the front controller, relative to docRoot: what nginx falls back to
	noFPM   bool   // the fpm row of another target covers this one
	suite   string // "fullstack" or "api"; empty for Gluo, which is in both

	listPages [2]string // the list's first two pages, with the build's own paging
	editPath  string    // a product's edit page, %d its id (forms and Inertia)
	afterSave string    // where a saved form redirects to (forms); the list when empty
	reseed    string    // the build's own reset and seed, run in the project before every run
	reseedEnv []string
	db        [3]string // compose service, database and product table: where products 6..100 go
}

// The steps of the journey: the login page, the login, the list as two pages of 50, a random
// product, a new price for it, a new product, the two pages again
var benchSteps = []string{"page", "login", "list1", "list2", "get", "edit", "add", "relist1", "relist2"}

// benchProducts is how many products the database has before a run
const benchProducts = 100

func randomProduct() int { return rand.IntN(benchProducts) + 1 }

func randomPrice() float64 { return math.Round((1+rand.Float64()*998)*100) / 100 }

func randomName() string { return fmt.Sprintf("Bench %d", rand.Int64()) }

func priceText(price float64) string { return strconv.FormatFloat(price, 'f', 2, 64) }

// benchWrites is what the journeys of a run sent to be stored, checked in the database afterwards
type benchWrites struct {
	mu     sync.Mutex
	prices map[int][]string  // the prices sent for each product: clients may edit the same one
	added  map[string]string // the new products: name, price
}

func newBenchWrites() *benchWrites {
	return &benchWrites{prices: map[int][]string{}, added: map[string]string{}}
}

func (w *benchWrites) edit(id int, price float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prices[id] = append(w.prices[id], priceText(price))
}

func (w *benchWrites) add(name string, price float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.added[name] = priceText(price)
}

func timed(steps *[]time.Duration, request func() error) error {
	start := time.Now()
	err := request()
	*steps = append(*steps, time.Since(start))
	return err
}

// journeyForms: Symfony, CakePHP and CodeIgniter, server-rendered forms with CSRF
func journeyForms(b *browser, target benchTarget) ([]time.Duration, error) {
	fw := target.framework
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

	if err := timed(&steps, func() error {
		p, err := b.trySubmit(form, map[string]string{fw.userField: adminEmail, fw.passwordField: adminPassword}, fw.loginPath)
		if err == nil && (p.status != http.StatusOK || p.path == fw.loginPath) {
			err = fmt.Errorf("login failed: %d %s", p.status, p.path)
		}
		return err
	}); err != nil {
		return steps, err
	}

	list := func(path string) func() error {
		return func() error {
			p, err := b.try(http.MethodGet, path, nil, nil)
			// Every product row links to its edit page
			if err == nil && (p.status != http.StatusOK || p.path != fw.listPath || !strings.Contains(p.body, "/edit")) {
				err = fmt.Errorf("no product list: %d %s", p.status, p.path)
			}
			return err
		}
	}
	return steps, crudSteps(&steps, target, list, crudFormSteps(b, target))
}

// crudFormSteps: a product's edit form, posted with a new price, and the new-product form, posted
func crudFormSteps(b *browser, target benchTarget) [3]func() error {
	fw := target.framework
	var edit htmlForm
	var from string
	var id int
	return [3]func() error{
		func() error {
			id = randomProduct()
			from = fmt.Sprintf(target.editPath, id)
			p, err := b.try(http.MethodGet, from, nil, nil)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(forms(p.body), hasField(fw.nameField))
			if p.status != http.StatusOK || i < 0 {
				return fmt.Errorf("no edit form at %s: %d %s", from, p.status, p.path)
			}
			edit = forms(p.body)[i]
			return nil
		},
		func() error {
			price := randomPrice()
			b.writes.edit(id, price)
			p, err := b.trySubmit(edit, map[string]string{fw.priceField: priceText(price)}, from)
			if err == nil && (p.status != http.StatusOK || p.path != cmpOr(target.afterSave, fw.listPath)) {
				err = fmt.Errorf("edit failed: %d %s", p.status, p.path)
			}
			return err
		},
		func() error {
			p, err := b.try(http.MethodGet, fw.newPath, nil, nil)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(forms(p.body), hasField(fw.nameField))
			if i < 0 {
				return fmt.Errorf("no new-product form: %d %s", p.status, p.path)
			}
			name, price := randomName(), randomPrice()
			b.writes.add(name, price)
			p, err = b.trySubmit(forms(p.body)[i], map[string]string{fw.nameField: name, fw.priceField: priceText(price)}, fw.newPath)
			if err == nil && (p.status != http.StatusOK || p.path != cmpOr(target.afterSave, fw.listPath)) {
				err = fmt.Errorf("add failed: %d %s", p.status, p.path)
			}
			return err
		},
	}
}

// crudSteps times the journey after the login: two pages, get, edit, add, two pages again
func crudSteps(steps *[]time.Duration, target benchTarget, list func(path string) func() error, getEditAdd [3]func() error) error {
	for _, step := range []func() error{list(target.listPages[0]), list(target.listPages[1]),
		getEditAdd[0], getEditAdd[1], getEditAdd[2], list(target.listPages[0]), list(target.listPages[1])} {
		if err := timed(steps, step); err != nil {
			return err
		}
	}
	return nil
}

// trySubmit posts a form with some of its fields changed, as a browser does
func (b *browser) trySubmit(form htmlForm, values map[string]string, from string) (page, error) {
	for name, value := range values {
		form.fields.Set(name, value)
	}
	action := cmpOr(form.action, from)
	if u, err := url.Parse(action); err == nil && u.IsAbs() {
		action = u.RequestURI()
	}
	return b.try(cmpOr(form.method, http.MethodPost), action, strings.NewReader(form.fields.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

// journeyInertia: Laravel's React screens, which log in and navigate with JSON requests
func journeyInertia(b *browser, target benchTarget) ([]time.Duration, error) {
	fw := target.framework
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

	list := func(path string) func() error {
		return func() error {
			p, ip, err := inertia(http.MethodGet, path, nil)
			if products, _ := ip.Props["products"].([]any); err == nil && (p.status != http.StatusOK || len(products) == 0) {
				err = fmt.Errorf("no product list: %d %s", p.status, p.path)
			}
			return err
		}
	}

	// The edit page, a PUT with a new price, a POST of a new product: what the React screens send
	var id int
	var name string
	saved := func(p page, err error, what string) error {
		if err == nil && (p.status != http.StatusOK || p.path != fw.listPath) {
			err = fmt.Errorf("%s failed: %d %s", what, p.status, p.path)
		}
		return err
	}
	return steps, crudSteps(&steps, target, list, [3]func() error{
		func() error {
			id = randomProduct()
			p, ip, err := inertia(http.MethodGet, fmt.Sprintf(target.editPath, id), nil)
			product, _ := ip.Props["product"].(map[string]any)
			name, _ = product["name"].(string)
			if err == nil && (p.status != http.StatusOK || name == "") {
				err = fmt.Errorf("no edit page for %d: %d %s", id, p.status, p.path)
			}
			return err
		},
		func() error {
			price := randomPrice()
			b.writes.edit(id, price)
			p, _, err := inertia(http.MethodPut, fmt.Sprintf("%s/%d", fw.listPath, id), map[string]any{"name": name, "price": price})
			return saved(p, err, "edit")
		},
		func() error {
			name, price := randomName(), randomPrice()
			b.writes.add(name, price)
			p, _, err := inertia(http.MethodPost, fw.listPath, map[string]any{"name": name, "price": price})
			return saved(p, err, "add")
		},
	})
}

// journeyAPI: Gluo. Its login page is the React SPA, a static file: no PHP for that step
func journeyAPI(b *browser, target benchTarget) ([]time.Duration, error) {
	fw := target.framework
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

	list := func(path string) func() error {
		return func() error {
			p, err := b.try(http.MethodGet, path, nil, map[string]string{"Accept": "application/json", "Authorization": "Bearer " + token})
			var products []any
			if err == nil {
				_ = json.Unmarshal([]byte(p.body), &products)
			}
			if err == nil && (p.status != http.StatusOK || len(products) == 0) {
				err = fmt.Errorf("no product list: %d %.100s", p.status, p.body)
			}
			return err
		}
	}

	// GET, PUT with a new price, POST of a new product: the SPA's calls
	var id int
	var name string
	written := func(p page, err error, what string) error {
		if err == nil && (p.status < 200 || p.status > 299) {
			err = fmt.Errorf("%s failed: %d %.100s", what, p.status, p.body)
		}
		return err
	}
	return steps, crudSteps(&steps, target, list, [3]func() error{
		func() error {
			id = randomProduct()
			p, data, err := b.tryAPI(http.MethodGet, fmt.Sprintf("%s/%d", fw.listPath, id), token, nil)
			name, _ = data["name"].(string)
			if err == nil && (p.status != http.StatusOK || name == "") {
				err = fmt.Errorf("no product %d: %d %.100s", id, p.status, p.body)
			}
			return err
		},
		func() error {
			price := randomPrice()
			b.writes.edit(id, price)
			p, _, err := b.tryAPI(http.MethodPut, fmt.Sprintf("%s/%d", fw.listPath, id), token, map[string]any{"name": name, "price": price})
			return written(p, err, "edit")
		},
		func() error {
			name, price := randomName(), randomPrice()
			b.writes.add(name, price)
			p, _, err := b.tryAPI(http.MethodPost, fw.listPath, token, map[string]any{"name": name, "price": price})
			return written(p, err, "add")
		},
	})
}

func (b *browser) tryAPI(method, path, token string, body any) (page, map[string]any, error) {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
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
func runJourneys(t *testing.T, target benchTarget, addr string, n, c int, writes *benchWrites) benchResult {
	result := benchResult{journeys: n, steps: make([][]time.Duration, len(benchSteps))}
	var mu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup
	start := time.Now()
	for range c {
		wg.Go(func() {
			for range jobs {
				b := newBrowser(t, target.baseURL, addr)
				b.writes = writes
				begin := time.Now()
				steps, err := target.journey(b, target)
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

// benchContainer starts the server's container, the same for every server but the image: user,
// mounts and settings. It is stopped when the test ends; it returns its name.
func benchContainer(t *testing.T, root string, target benchTarget, image, script string) string {
	here, _ := filepath.Abs(".")
	name := "warmphp-bench-" + strconv.Itoa(os.Getpid())
	cmd := []string{"docker", "run", "--rm", "-d", "--init", "--name", name, "--network", "host",
		"-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", root + ":" + root, "-v", here + ":" + here, "-v", "/tmp:/tmp", "-e", "PROJECT_ROOT=" + root}
	for _, env := range target.env {
		cmd = append(cmd, "-e", env)
	}
	cmd = append(cmd, "--entrypoint", "sh", image, "-c", script)
	if out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "stop", name).Run() })
	return name
}

// benchImage has php, php-fpm and nginx: FPM and the gateway's workers run the same PHP build
func benchImage() string {
	return cmpOr(os.Getenv("BENCH_IMAGE"), "byjg/php:8.5-fpm-nginx")
}

// benchINI is the PHP configuration of every server: OPcache, and BENCH_PHP_INI
func benchINI(opcache string) [][2]string {
	settings := [][2]string{{"opcache.enable", opcache}, {"opcache.enable_cli", opcache}}
	for _, setting := range strings.Fields(cmpOr(os.Getenv("BENCH_PHP_INI"), "xdebug.mode=off")) {
		name, value, _ := strings.Cut(setting, "=")
		settings = append(settings, [2]string{name, value})
	}
	return settings
}

// benchDefines is benchINI as php and php-fpm options
func benchDefines(opcache string) string {
	var defines []string
	for _, setting := range benchINI(opcache) {
		defines = append(defines, "-d "+setting[0]+"="+setting[1])
	}
	return strings.Join(defines, " ")
}

// benchAddr is where every server listens
const benchAddr = "127.0.0.1:18811"

// benchNginx writes the nginx configuration FPM and the gateway share (the image's defaults: worker
// processes, mime types) around server, the part that differs, and returns the command that runs
// it. The image's own start scripts listen on port 80 as its user; here nginx runs as the user
// that owns the projects, on the host network, so every path it writes is in dir.
func benchNginx(t *testing.T, dir, server string) string {
	conf := filepath.Join(dir, "nginx.conf")
	text := fmt.Sprintf(`worker_processes auto;
pid %[1]s/nginx.pid;
error_log stderr warn;
events { worker_connections 1024; }
http {
    include /etc/nginx/mime.types;
    access_log off;
    client_max_body_size 1m;
    client_body_temp_path %[1]s/client;
    proxy_temp_path %[1]s/proxy;
    fastcgi_temp_path %[1]s/fastcgi;
    uwsgi_temp_path %[1]s/uwsgi;
    scgi_temp_path %[1]s/scgi;
%[2]s
}
`, dir, server)
	if err := os.WriteFile(conf, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return "exec nginx -e stderr -c " + conf + " -g 'daemon off;'"
}

func waitHTTP(t *testing.T, addr, host string) {
	t.Helper()
	// Any answer but nginx's 502 (the backend still booting) means the server is up; a redirect
	// (CodeIgniter's /) goes to the configured host
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for range 240 {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
		req.Host = host
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("nothing answers on %s", addr)
}

// startFPM serves the target with nginx and PHP-FPM: C static children, the project's front
// controller as nginx's fallback, as the image does with NGINX_ROOT and PHP_CONTROLLER.
// variables_order=EGPCS: the settings are environment variables, which Symfony's Dotenv only
// reads from $_SERVER and $_ENV.
func startFPM(t *testing.T, target benchTarget, root, opcache string, c int) string {
	dir := t.TempDir()
	pool := filepath.Join(dir, "php-fpm.conf")
	if err := os.WriteFile(pool, []byte(fmt.Sprintf(`[global]
pid = %[1]s/php-fpm.pid
error_log = /proc/self/fd/2
[www]
listen = %[1]s/php-fpm.sock
pm = static
pm.max_children = %[2]d
clear_env = no
catch_workers_output = yes
decorate_workers_output = no
`, dir, c)), 0o644); err != nil {
		t.Fatal(err)
	}
	nginx := benchNginx(t, dir, fmt.Sprintf(`    server {
        listen %s;
        root %s;
        location / {
            try_files $uri %s?$query_string;
        }
        location ~ \.php$ {
            fastcgi_pass unix:%s/php-fpm.sock;
            include /etc/nginx/fastcgi_params;
            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        }
    }`, benchAddr, filepath.Join(root, target.docRoot), target.front, dir))
	name := benchContainer(t, root, target, benchImage(),
		fmt.Sprintf("php-fpm -y %s -D %s -d variables_order=EGPCS && %s", pool, benchDefines(opcache), nginx))
	u, _ := url.Parse(target.baseURL)
	waitHTTP(t, benchAddr, u.Host)
	return name
}

// benchGateway builds the gateway once, static, to run in the PHP image, unless BENCH_GATEWAY has one
var benchGateway = sync.OnceValues(func() (string, error) {
	if path := os.Getenv("BENCH_GATEWAY"); path != "" {
		return filepath.Abs(path)
	}
	path := filepath.Join(os.TempDir(), "warmphp-bench-gateway")
	cmd := exec.Command("go", "build", "-o", path, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v %s", err, out)
	}
	return path, nil
})

// startWorkers serves the target with nginx and the gateway: C workers (its child processes, the
// frames go through plain pipes) and the boot file of compat/
func startWorkers(t *testing.T, target benchTarget, root, opcache string, c int) string {
	return startBenchGateway(t, target, root, opcache, c, "")
}

// startPrefork is startWorkers with -prefork: one master boots the application and forks the C
// workers, which share its OPcache and memory and talk to the gateway over a Unix socket
func startPrefork(t *testing.T, target benchTarget, root, opcache string, c int) string {
	return startBenchGateway(t, target, root, opcache, c, "-prefork")
}

func startBenchGateway(t *testing.T, target benchTarget, root, opcache string, c int, flags string) string {
	gateway, err := benchGateway()
	if err != nil {
		t.Fatal(err)
	}
	here, _ := filepath.Abs(".")
	listen := "127.0.0.1:18812"
	dir := t.TempDir()
	nginx := benchNginx(t, dir, fmt.Sprintf(`    upstream gateway {
        server %s;
        keepalive 16;
    }
    server {
        listen %s;
        location / {
            proxy_pass http://gateway;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_set_header Host $http_host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }
    }`, listen, benchAddr))
	name := benchContainer(t, root, target, benchImage(), fmt.Sprintf(
		"%s %s -listen %s -workers %d -max-requests %d -timeout 60s -boot-timeout 120s -max-body %d -upload-dir %s -php 'php %s' -worker %s %s & %s",
		gateway, flags, listen, c, target.maxRequests, 1<<20, dir, benchDefines(opcache),
		filepath.Join(here, "php/worker.php"), filepath.Join(here, "compat", target.boot), nginx))
	u, _ := url.Parse(target.baseURL)
	waitHTTP(t, benchAddr, u.Host)
	return name
}

// benchFrankenImage builds FrankenPHP's image with the builds' extensions, once
var benchFrankenImage = sync.OnceValues(func() (string, error) {
	image := "warmphp-bench-frankenphp"
	if out, err := exec.Command("docker", "build", "-q", "-t", image, "benchmark/frankenphp").CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v %s", err, out)
	}
	return image, nil
})

// startFrankenPHP serves the target with FrankenPHP's worker mode: C threads, each running
// compat/frankenphp.php, which boots the same boot file of compat/ once and handles every request.
func startFrankenPHP(t *testing.T, target benchTarget, root, opcache string, c int) string {
	here, _ := filepath.Abs(".")
	return benchFranken(t, target, root, opcache, c, fmt.Sprintf(`php_server {
		worker {
			file %[1]s
			num %[2]d
			match *
			env WARMPHP_BOOT %[3]s
			env WARMPHP_MAX_REQUESTS %[4]d
		}
	}`, filepath.Join(here, "compat/frankenphp.php"), c, target.boot, target.maxRequests))
}

// startFrankenClassic serves the target with FrankenPHP's classic mode: C threads running the
// build's own front controller on every request, as FPM does
func startFrankenClassic(t *testing.T, target benchTarget, root, opcache string, c int) string {
	return benchFranken(t, target, root, opcache, c, fmt.Sprintf(`php_server {
		index %s
		try_files {path} %s
	}`, strings.TrimPrefix(target.front, "/"), target.front))
}

// benchFranken runs FrankenPHP with C threads and the given PHP site directive. Its own Caddyfile,
// as nginx's: no admin API, no HTTPS, no compression, and Caddy's data in dir.
func benchFranken(t *testing.T, target benchTarget, root, opcache string, c int, php string) string {
	image, err := benchFrankenImage()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var ini []string
	for _, setting := range append(benchINI(opcache), [2]string{"variables_order", "EGPCS"}) {
		ini = append(ini, "\t\tphp_ini "+setting[0]+" "+setting[1])
	}
	caddyfile := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte(fmt.Sprintf(`{
	admin off
	auto_https off
	persist_config off
	frankenphp {
		num_threads %[1]d
%[2]s
	}
}

http://:%[3]s {
	bind 127.0.0.1
	root %[4]s
	%[5]s
}
`, c, strings.Join(ini, "\n"), strings.Split(benchAddr, ":")[1], filepath.Join(root, target.docRoot), php)), 0o644); err != nil {
		t.Fatal(err)
	}
	name := benchContainer(t, root, target, image, fmt.Sprintf(
		"export XDG_CONFIG_HOME=%[1]s XDG_DATA_HOME=%[1]s && exec frankenphp run --config %[2]s --adapter caddyfile", dir, caddyfile))
	u, _ := url.Parse(target.baseURL)
	waitHTTP(t, benchAddr, u.Host)
	return name
}

// benchCPU is the machine's CPU counters: its busy, stolen and total time
func benchCPU(t *testing.T) (busy, steal, total float64) {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.SplitN(string(stat), "\n", 2)[0])[1:]
	for i, field := range fields[:8] { // user nice system idle iowait irq softirq steal
		value, _ := strconv.ParseFloat(field, 64)
		total += value
		if i != 3 && i != 4 {
			busy += value
		}
		if i == 7 {
			steal = value
		}
	}
	return busy, steal, total
}

// benchMemory is the memory the server's container uses, as docker stats shows it (without the
// page cache)
func benchMemory(t *testing.T, name string) string {
	out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", name).Output()
	if err != nil {
		t.Errorf("docker stats: %v", err)
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(strings.Split(string(out), "/")[0]), " ", "")
}

func TestBenchmark(t *testing.T) {
	journeys, _ := strconv.Atoi(cmpOr(os.Getenv("BENCH_JOURNEYS"), "3000"))
	clients, _ := strconv.Atoi(cmpOr(os.Getenv("BENCH_CLIENTS"), "4"))
	laravelEnv := []string{"DB_HOST=127.0.0.1", "DB_PORT=3307", "CACHE_STORE=array"}
	// The API builds run with the settings of their full-stack siblings (their .env has the database)
	apiLaravelEnv := []string{"CACHE_STORE=array"}
	servers := strings.Split(cmpOr(os.Getenv("BENCH_SERVERS"), "fpm,frankenphp-classic,worker,prefork,frankenphp"), ",")
	start := map[string]func(*testing.T, benchTarget, string, string, int) string{
		"fpm": startFPM, "frankenphp-classic": startFrankenClassic, "worker": startWorkers, "prefork": startPrefork,
		"frankenphp": startFrankenPHP,
	}
	for _, server := range servers {
		if start[server] == nil {
			t.Fatalf("BENCH_SERVERS: unknown server %q", server)
		}
	}

	// Every run resets each build with its own documented commands: tables recreated, so the
	// ids start at 1, and the build's 5 seeded products
	cakeReseed := "bin/cake migrations rollback -t 0 && bin/cake migrations migrate && bin/cake seeds run -q -f"
	// The debug toolbar writes a file per request in development: the folder must not grow run after run
	codeigniterReseed := "php spark migrate:refresh --all -f && php spark db:seed ProductSeeder && " +
		"find writable/debugbar -name 'debugbar_*' -delete && find writable/session -name 'ci_session*' -delete"
	// Not fixtures:load --purge-with-truncate: in MySQL, TRUNCATE ends Doctrine's transaction
	symfonyReseed := "php bin/console doctrine:schema:drop --force --full-database -q && " +
		"php bin/console doctrine:migrations:migrate -n -q && php bin/console doctrine:fixtures:load -n -q"
	// Inertia's DevTools rewrites an index of every entry on each request, under a lock: the cost
	// grows with the folder, and its 24h expiry makes it fall in the middle of a run
	laravelReseed := "php artisan migrate:fresh --seed --force && rm -rf storage/inertia-devtools"
	apiPages := [2]string{"/product?page=0&size=50", "/product?page=1&size=50"}

	targets := []benchTarget{
		{framework: framework{name: "symfony", project: "frameworks/fullstack/symfony-official", boot: "symfony.php", baseURL: "http://localhost:8083",
			env: []string{"APP_ENV=prod", "APP_DEBUG=0"}, loginPath: "/login", listPath: "/product", newPath: "/product/new",
			userField: "_username", passwordField: "_password", nameField: "product[name]", priceField: "product[price]"},
			journey: journeyForms, docRoot: "public", front: "/index.php", suite: "fullstack",
			// no paging: the list is the whole table
			listPages: [2]string{"/product", "/product"}, editPath: "/product/%d/edit",
			reseed: symfonyReseed, db: [3]string{"database", "app", "product"}},
		{framework: framework{name: "cakephp", project: "frameworks/fullstack/cakephp", boot: "cakephp.php", baseURL: "http://localhost:8084",
			env: []string{"DEBUG=false", "APP_FULL_BASE_URL=http://localhost:8084"}, loginPath: "/users/login", listPath: "/products",
			newPath: "/products/add", userField: "email", passwordField: "password", nameField: "name", priceField: "price"},
			journey: journeyForms, docRoot: "webroot", front: "/index.php", suite: "fullstack",
			// the build routes / to the list, so redirect(['action' => 'index']) answers /
			listPages: [2]string{"/products?page=1&limit=50", "/products?page=2&limit=50"}, editPath: "/products/edit/%d", afterSave: "/",
			reseed: cakeReseed, db: [3]string{"database", "app", "products"}},
		{framework: framework{name: "codeigniter", project: "frameworks/fullstack/codeigniter", boot: "codeigniter.php", baseURL: "http://localhost:8085",
			loginPath: "/login", listPath: "/products", newPath: "/products/new", userField: "email", passwordField: "password",
			nameField: "name", priceField: "price"},
			journey: journeyForms, docRoot: "public", front: "/index.php", suite: "fullstack",
			// paginate(20): its page size is fixed
			listPages: [2]string{"/products?page=1", "/products?page=2"}, editPath: "/products/edit/%d",
			reseed: codeigniterReseed, db: [3]string{"database", "app", "products"}},
		{framework: framework{name: "laravel-fresh", project: "frameworks/fullstack/laravel", boot: "laravel-fresh.php", baseURL: "http://localhost:8081",
			env: laravelEnv, loginPath: "/login", listPath: "/products", maxRequests: 30},
			journey: journeyInertia, docRoot: "public", front: "/index.php", suite: "fullstack",
			// no paging: the list is the whole table
			listPages: [2]string{"/products", "/products"}, editPath: "/products/%d/edit",
			reseed: laravelReseed, reseedEnv: laravelEnv, db: [3]string{"mysql", "laravel", "products"}},
		{framework: framework{name: "laravel-shared", project: "frameworks/fullstack/laravel", boot: "laravel-shared.php", baseURL: "http://localhost:8081",
			env: laravelEnv, loginPath: "/login", listPath: "/products"},
			journey: journeyInertia, noFPM: true, suite: "fullstack",
			listPages: [2]string{"/products", "/products"}, editPath: "/products/%d/edit",
			reseed: laravelReseed, reseedEnv: laravelEnv, db: [3]string{"mysql", "laravel", "products"}},
		{framework: framework{name: "gluo", project: "frameworks/gluo", boot: "gluo.php", baseURL: "http://localhost:8080",
			env: []string{"APP_ENV=dev"}, loginPath: "/login", listPath: "/product", requires: gluoApplication},
			journey: journeyAPI, docRoot: "public", front: "/app.php",
			listPages: apiPages, reseed: "php gluo --env=dev migrate:reset", db: [3]string{"gluo-database", "localdev", "product"}},

		// The same frameworks as JSON APIs in front of Gluo's SPA: the journey is Gluo's
		{framework: framework{name: "api-symfony", project: "frameworks/api/symfony", boot: "symfony.php", baseURL: "http://localhost:9082",
			env: []string{"APP_ENV=prod", "APP_DEBUG=0"}, loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, docRoot: "public", front: "/index.php", suite: "api",
			listPages: apiPages, reseed: symfonyReseed, db: [3]string{"database", "app", "product"}},
		{framework: framework{name: "api-cakephp", project: "frameworks/api/cakephp", boot: "cakephp.php", baseURL: "http://localhost:9083",
			env: []string{"DEBUG=false", "APP_FULL_BASE_URL=http://localhost:9083"}, loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, docRoot: "webroot", front: "/index.php", suite: "api",
			listPages: apiPages, reseed: cakeReseed, db: [3]string{"database", "app", "products"}},
		{framework: framework{name: "api-codeigniter", project: "frameworks/api/codeigniter", boot: "codeigniter.php", baseURL: "http://localhost:9084",
			loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, docRoot: "public", front: "/index.php", suite: "api",
			listPages: apiPages, reseed: codeigniterReseed, db: [3]string{"database", "app", "products"}},
		{framework: framework{name: "api-laravel-fresh", project: "frameworks/api/laravel", boot: "laravel-fresh.php", baseURL: "http://localhost:9081",
			env: apiLaravelEnv, loginPath: "/login", listPath: "/product", maxRequests: 30},
			journey: journeyAPI, docRoot: "public", front: "/index.php", suite: "api",
			listPages: apiPages, reseed: laravelReseed, db: [3]string{"mysql", "laravel", "products"}},
		{framework: framework{name: "api-laravel-shared", project: "frameworks/api/laravel", boot: "laravel-shared.php", baseURL: "http://localhost:9081",
			env: apiLaravelEnv, loginPath: "/login", listPath: "/product"},
			journey: journeyAPI, noFPM: true, suite: "api",
			listPages: apiPages, reseed: laravelReseed, db: [3]string{"mysql", "laravel", "products"}},
	}

	out := filepath.Join("benchmark", "results.tsv")
	_ = os.MkdirAll("benchmark", 0o755)
	if _, err := os.Stat(out); errors.Is(err, os.ErrNotExist) || os.Getenv("BENCH_ONLY") == "" {
		header := append(append([]string{"suite", "target", "server", "opcache", "journeys", "errors", "journeys/s", "p50", "p99", "memory", "cpu", "steal"},
			benchSteps...), "first error")
		_ = os.WriteFile(out, []byte(strings.Join(header, "\t")+"\n"), 0o644)
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
		// In a random order: the first run after the database starts should not always be the same server
		type benchRun struct{ server, opcache string }
		var runs []benchRun
		for _, opcache := range []string{"1", "0"} {
			for _, server := range servers {
				// The servers that run the build's own front controller: one row for the Laravel variants
				if (server != "fpm" && server != "frankenphp-classic") || !target.noFPM {
					runs = append(runs, benchRun{server, opcache})
				}
			}
		}
		rand.Shuffle(len(runs), func(i, j int) { runs[i], runs[j] = runs[j], runs[i] })
		for _, run := range runs {
			server, opcache := run.server, run.opcache
			label := map[string]string{"1": "on", "0": "off"}[opcache]
			t.Run(fmt.Sprintf("%s/%s/opcache-%s", target.name, server, label), func(t *testing.T) {
				reseed(t, target, root)
				container := start[server](t, target, root, opcache, clients)
				writes := newBenchWrites()
				runJourneys(t, target, benchAddr, max(clients, journeys/20), clients, writes) // warm-up
				busy, steal, total := benchCPU(t)
				r := runJourneys(t, target, benchAddr, journeys, clients, writes)
				busyEnd, stealEnd, totalEnd := benchCPU(t)
				memory := benchMemory(t, container)
				stored := verifyWrites(target, root, writes)
				if stored != nil {
					r.firstError = cmpOr(r.firstError, stored.Error())
				}

				stepMeans := make([]string, len(benchSteps))
				for i := range stepMeans {
					stepMeans[i] = mean(r.steps[i]).Round(10 * time.Microsecond).String()
				}
				// The servers that run the front controller have one Laravel row per suite: an
				// application per request
				name := target.name
				if server == "fpm" || server == "frankenphp-classic" {
					name = strings.TrimSuffix(name, "-fresh")
				}
				fields := []string{cmpOr(target.suite, "both"), name, server, label, strconv.Itoa(r.journeys), strconv.Itoa(r.errors),
					fmt.Sprintf("%.3f", float64(r.journeys-r.errors)/r.elapsed.Seconds()),
					percentile(r.totals, 0.50).Round(10 * time.Microsecond).String(),
					percentile(r.totals, 0.99).Round(10 * time.Microsecond).String(), memory,
					fmt.Sprintf("%.0f%%", 100*(busyEnd-busy)/(totalEnd-total)),
					fmt.Sprintf("%.1f%%", 100*(stealEnd-steal)/(totalEnd-total))}
				line := strings.Join(append(append(fields, stepMeans...), strings.Join(strings.Fields(r.firstError), " ")), "\t")
				write(line)
				t.Log(line)
				if r.errors > 0 {
					t.Errorf("%d of %d journeys failed, first: %s", r.errors, r.journeys, r.firstError)
				}
				if stored != nil {
					t.Errorf("journeys failed: %v", stored)
				}
			})
		}
	}
}

// reseed resets the build with its own commands, then adds products 6..benchProducts with one SQL
// insert through its database service (the root password is the container's own MYSQL_ROOT_PASSWORD)
func reseed(t *testing.T, target benchTarget, root string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", target.reseed)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), target.reseedEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reseed %s: %v %s", target.name, err, out)
	}

	table := target.db[2]
	insert := fmt.Sprintf("INSERT INTO %s (name, price, created_at, updated_at) "+
		"WITH RECURSIVE n(i) AS (SELECT 6 UNION ALL SELECT i + 1 FROM n WHERE i < %d) "+
		"SELECT CONCAT('Product ', LPAD(i, 3, '0')), 1 + (i * 37) %% 1000 + 0.99, NOW(), NOW() FROM n;\n"+
		"SELECT COUNT(*), MIN(id), MAX(id) FROM %s WHERE deleted_at IS NULL;\n", table, benchProducts, table)
	out, err := benchSQL(target, root, insert)
	if got := strings.Fields(string(out)); err != nil || len(got) != 3 || got[0] != strconv.Itoa(benchProducts) || got[1] != "1" {
		t.Fatalf("%s: expected %d products, ids from 1 (count, min, max): %q %v", target.name, benchProducts, got, err)
	}
}

// benchSQL runs statements through the build's database service
func benchSQL(target benchTarget, root, statements string) ([]byte, error) {
	cmd := exec.Command("docker", "compose", "exec", "-T", target.db[0], "sh", "-c", `mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -N `+target.db[1])
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(statements)
	return cmd.Output()
}

// verifyWrites checks that the database has what the journeys sent: each edited product has one of
// the prices sent for it, every new product is there with its price, and nothing else appeared
func verifyWrites(target benchTarget, root string, writes *benchWrites) error {
	out, err := benchSQL(target, root, fmt.Sprintf("SELECT id, name, price FROM %s WHERE deleted_at IS NULL;\n", target.db[2]))
	if err != nil {
		return fmt.Errorf("reading the products: %v", err)
	}
	products, added := 0, 0
	for line := range strings.Lines(string(out)) {
		columns := strings.Split(strings.TrimSpace(line), "\t")
		if len(columns) != 3 {
			return fmt.Errorf("unexpected product row: %q", line)
		}
		id, _ := strconv.Atoi(columns[0])
		price, err := strconv.ParseFloat(columns[2], 64)
		if err != nil {
			return fmt.Errorf("product %d: price %q", id, columns[2])
		}
		stored := priceText(price)
		if id > benchProducts {
			sent, ok := writes.added[columns[1]]
			if !ok || sent != stored {
				return fmt.Errorf("product %d %q: stored %s, sent %q", id, columns[1], stored, sent)
			}
			added++
			continue
		}
		products++
		if sent := writes.prices[id]; len(sent) > 0 && !slices.Contains(sent, stored) {
			return fmt.Errorf("product %d: stored %s, sent %v", id, stored, sent)
		}
	}
	if products != benchProducts || added != len(writes.added) {
		return fmt.Errorf("%d seeded products and %d new, expected %d and %d", products, added, benchProducts, len(writes.added))
	}
	return nil
}
