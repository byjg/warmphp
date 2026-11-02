//go:build compat

package main

// Runs the builds of github.com/byjg/gluo-framework-comparison behind the gateway, without
// changing them, through the boot files in compat/. Each project's MySQL must be running
// and seeded (see docs/compatibility.md).
//
//   PHPWORKER_IMAGE=byjg/php:8.5-cli-load go test -tags compat -run TestCompat -v
//
// COMPAT_DIR points to the comparison repository (default ../gluo-framework-comparison).
// COMPAT_GLUO_STARTER points to a project created from the byjg/gluo starter, with its test
// database migrated (php gluo test does it).

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	adminEmail    = "admin@example.com"
	adminPassword = "!P4ssw0rdstr!"
)

// gluoApplication is the class gluo.php needs: byjg/gluo-core 7.0's HTTP entry point
const gluoApplication = "vendor/byjg/gluo-core/src/Http/Application.php"

type framework struct {
	name    string // the subtest; the boot file's name when empty
	project string // folder in the comparison repository, or an absolute path
	// requires is a file the project must have for its boot file to work (the test skips otherwise)
	requires string
	boot     string   // boot file in compat/
	baseURL  string   // the URL the project is configured for; requests are sent to the gateway
	env      []string // extra environment variables for PHP
	// maxRequests replaces a worker after this many requests (0 = never)
	maxRequests int
	run         func(t *testing.T, fw framework, gw string)

	loginPath, listPath, newPath, logoutPath        string
	userField, passwordField, nameField, priceField string
}

func compatDir(t *testing.T) string {
	dir := os.Getenv("COMPAT_DIR")
	if dir == "" {
		dir = "../gluo-framework-comparison"
	}
	dir, _ = filepath.Abs(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("comparison repository not found at %s", dir)
	}
	return dir
}

// startCompatGateway runs one worker, so every request (and every leak) goes through the same process.
func startCompatGateway(t *testing.T, fw framework) string {
	t.Helper()
	root := fw.project
	if !filepath.IsAbs(root) {
		root = filepath.Join(compatDir(t), fw.project)
	}
	if fw.requires != "" {
		if _, err := os.Stat(filepath.Join(root, fw.requires)); err != nil {
			t.Skipf("%s has no %s", root, fw.requires)
		}
	}
	image := os.Getenv("PHPWORKER_IMAGE")
	if image == "" {
		image = "byjg/php:8.5-cli-load"
	}

	php := []string{"docker", "run", "--rm", "-i", "--init", "--network", "host", "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", root + ":" + root, "-v", "/tmp:/tmp"}
	for _, env := range fw.env {
		php = append(php, "-e", env)
	}
	// OPcache is off in the CLI by default; production would turn it on
	php = append(php, image, "php", "-d", "opcache.enable_cli=1")

	g, err := newGateway(config{
		php:         php,
		mode:        "callable",
		framework:   strings.TrimSuffix(fw.boot, ".php"),
		projectRoot: root,
		workers:     1,
		timeout:     60 * time.Second,
		bootTimeout: 120 * time.Second,
		maxBody:     1 << 20,
		uploadDir:   os.TempDir(),
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

// browser keeps cookies and follows redirects like a browser. Every connection goes to the
// gateway, whatever host the application puts in its links and redirects.
type browser struct {
	t       *testing.T
	base    string
	http    *http.Client
	current string // the page the browser is on, sent as Referer
}

func newBrowser(t *testing.T, base, gateway string) *browser {
	jar, _ := cookiejar.New(nil)
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, gateway)
	}}
	return &browser{t: t, base: base, http: &http.Client{Jar: jar, Transport: transport}}
}

type page struct {
	status int
	path   string // after redirects
	body   string
	header http.Header
}

func (b *browser) do(method, target string, body io.Reader, headers map[string]string) page {
	b.t.Helper()
	p, err := b.try(method, target, body, headers)
	if err != nil {
		b.t.Fatal(err)
	}
	return p
}

// try is do() returning the error, for the benchmark, which counts errors instead of stopping.
func (b *browser) try(method, target string, body io.Reader, headers map[string]string) (page, error) {
	if !strings.HasPrefix(target, "http") {
		target = b.base + target
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return page{}, err
	}
	if method != http.MethodGet {
		// Browsers send both on a form POST; Symfony's stateless CSRF check relies on them
		req.Header.Set("Origin", b.base)
		req.Header.Set("Referer", cmpOr(b.current, b.base+"/"))
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return page{}, fmt.Errorf("%s %s: %w", method, target, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	b.current = resp.Request.URL.String()
	return page{status: resp.StatusCode, path: resp.Request.URL.Path, body: string(data), header: resp.Header}, nil
}

func (b *browser) get(path string) page { return b.do(http.MethodGet, path, nil, nil) }

func (b *browser) cookie(name string) string {
	u, _ := url.Parse(b.base)
	for _, c := range b.http.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

type htmlForm struct {
	action, method string
	fields         url.Values
}

var (
	formRe  = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	inputRe = regexp.MustCompile(`(?is)<input\b([^>]*)>`)
	attrRe  = regexp.MustCompile(`(?is)([a-z_:-]+)\s*=\s*"([^"]*)"`)
)

func attrs(tag string) map[string]string {
	values := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
		values[strings.ToLower(m[1])] = html.UnescapeString(m[2])
	}
	return values
}

// forms returns the forms of a page with the values a browser would submit.
func forms(body string) []htmlForm {
	var result []htmlForm
	for _, m := range formRe.FindAllStringSubmatch(body, -1) {
		form := attrs(m[1])
		f := htmlForm{action: form["action"], method: strings.ToUpper(form["method"]), fields: url.Values{}}
		for _, input := range inputRe.FindAllStringSubmatch(m[2], -1) {
			a := attrs(input[1])
			if a["name"] == "" || a["type"] == "submit" || (a["type"] == "checkbox" && !strings.Contains(input[1], "checked")) {
				continue
			}
			f.fields.Add(a["name"], a["value"])
		}
		result = append(result, f)
	}
	return result
}

func findForm(t *testing.T, p page, match func(htmlForm) bool) htmlForm {
	t.Helper()
	for _, f := range forms(p.body) {
		if match(f) {
			return f
		}
	}
	t.Fatalf("form not found on %s (status %d): %.300s", p.path, p.status, p.body)
	return htmlForm{}
}

func hasField(name string) func(htmlForm) bool {
	return func(f htmlForm) bool { _, ok := f.fields[name]; return ok }
}

func (b *browser) submit(f htmlForm, values map[string]string, from string) page {
	b.t.Helper()
	for name, value := range values {
		f.fields.Set(name, value)
	}
	action := f.action
	if action == "" {
		action = from
	}
	if u, err := url.Parse(action); err == nil && u.IsAbs() {
		action = u.RequestURI()
	}
	return b.do(cmpOr(f.method, http.MethodPost), action, strings.NewReader(f.fields.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// rowLink finds, in a product list, the link to path segment `kind` (edit, delete...) of the row showing text.
func rowLink(t *testing.T, body, text, pattern string) (string, string) {
	t.Helper()
	i := strings.Index(body, text)
	if i < 0 {
		t.Fatalf("%q not found in the list", text)
	}
	end := min(len(body), i+3000)
	m := regexp.MustCompile(pattern).FindStringSubmatch(body[i:end])
	if m == nil {
		t.Fatalf("no link matching %s after %q", pattern, text)
	}
	link := html.UnescapeString(m[1])
	if u, err := url.Parse(link); err == nil && u.IsAbs() {
		link = u.RequestURI()
	}
	return link, m[2]
}

func expectLogin(t *testing.T, what string, p page, loginPath string) {
	t.Helper()
	if p.path != loginPath {
		t.Errorf("%s: expected the login page, got %d %s", what, p.status, p.path)
	}
}

// runForms is the scenario for the server-rendered builds (Symfony, CakePHP, CodeIgniter).
func runForms(t *testing.T, fw framework, gw string) {
	admin := newBrowser(t, fw.baseURL, gw)
	name := fmt.Sprintf("Worker test %d", time.Now().UnixNano())

	expectLogin(t, "anonymous list", admin.get(fw.listPath), fw.loginPath)

	login := admin.get(fw.loginPath)
	loginForm := findForm(t, login, hasField(fw.passwordField))
	failed := admin.submit(loginForm, map[string]string{fw.userField: adminEmail, fw.passwordField: "wrong"}, fw.loginPath)
	expectLogin(t, "wrong password", failed, fw.loginPath)

	loginForm = findForm(t, admin.get(fw.loginPath), hasField(fw.passwordField))
	loggedIn := admin.submit(loginForm, map[string]string{fw.userField: adminEmail, fw.passwordField: adminPassword}, fw.loginPath)
	if loggedIn.path == fw.loginPath {
		t.Fatalf("login failed: %d %s %.500s", loggedIn.status, loggedIn.path, loggedIn.body)
	}

	list := admin.get(fw.listPath)
	if list.status != http.StatusOK || list.path != fw.listPath {
		t.Fatalf("list after login: %d %s", list.status, list.path)
	}

	t.Run("no session leaks to another client", func(t *testing.T) {
		anonymous := newBrowser(t, fw.baseURL, gw)
		expectLogin(t, "anonymous list right after an admin request", anonymous.get(fw.listPath), fw.loginPath)
	})

	newForm := admin.get(fw.newPath)
	created := admin.submit(findForm(t, newForm, hasField(fw.nameField)), map[string]string{fw.nameField: name, fw.priceField: "12.34"}, fw.newPath)
	if !strings.Contains(admin.get(fw.listPath).body, name) {
		t.Fatalf("created product not in the list (create answered %d %s): %.500s", created.status, created.path, created.body)
	}

	list = admin.get(fw.listPath)
	editLink, id := rowLink(t, list.body, name, `href="([^"]*?/(\d+)/edit|[^"]*?/edit/(?:)(\d+))"`)
	if id == "" {
		id = regexp.MustCompile(`(\d+)`).FindString(editLink)
	}
	edit := admin.get(editLink)
	admin.submit(findForm(t, edit, hasField(fw.nameField)), map[string]string{fw.nameField: name + " edited"}, editLink)
	list = admin.get(fw.listPath)
	if !strings.Contains(list.body, name+" edited") {
		t.Fatalf("edited product not in the list")
	}

	// The delete button is a form on the list (CakePHP, CodeIgniter) or on the edit page (Symfony)
	isDelete := func(f htmlForm) bool {
		return f.method == http.MethodPost && (strings.Contains(f.action, "delete/"+id) || strings.HasSuffix(f.action, "/"+id))
	}
	deleteFrom := list
	if !slices.ContainsFunc(forms(list.body), isDelete) {
		deleteFrom = admin.get(editLink)
	}
	admin.submit(findForm(t, deleteFrom, isDelete), nil, deleteFrom.path)
	if strings.Contains(admin.get(fw.listPath).body, name+" edited") {
		t.Errorf("deleted product still in the list")
	}

	admin.get(fw.logoutPath)
	expectLogin(t, "list after logout", admin.get(fw.listPath), fw.loginPath)

	measure(t, fw, gw)
}

var dataPageRe = regexp.MustCompile(`(?s)data-page="([^"]+)"|<script[^>]*data-page="app"[^>]*>(.*?)</script>`)

type inertiaPage struct {
	Component string         `json:"component"`
	Props     map[string]any `json:"props"`
	URL       string         `json:"url"`
	Version   string         `json:"version"`
}

// inertia sends what Inertia's client sends: JSON bodies, X-Inertia headers and the XSRF token.
func (b *browser) inertia(method, path, version string, body any) (page, inertiaPage) {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = strings.NewReader(string(data))
	}
	xsrf, _ := url.QueryUnescape(b.cookie("XSRF-TOKEN"))
	p := b.do(method, path, reader, map[string]string{
		"Content-Type":      "application/json",
		"Accept":            "text/html, application/xhtml+xml",
		"X-Requested-With":  "XMLHttpRequest",
		"X-Inertia":         "true",
		"X-Inertia-Version": version,
		"X-XSRF-TOKEN":      xsrf,
	})
	var ip inertiaPage
	_ = json.Unmarshal([]byte(p.body), &ip)
	return p, ip
}

func firstVisit(t *testing.T, b *browser, path string) (page, inertiaPage) {
	t.Helper()
	p := b.get(path)
	m := dataPageRe.FindStringSubmatch(p.body)
	if m == nil {
		t.Fatalf("no Inertia page in %s (%d): %.300s", path, p.status, p.body)
	}
	var ip inertiaPage
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1]+m[2])), &ip); err != nil {
		t.Fatal(err)
	}
	return p, ip
}

func productNamed(ip inertiaPage, name string) (map[string]any, bool) {
	products, _ := ip.Props["products"].([]any)
	for _, p := range products {
		if product, ok := p.(map[string]any); ok && product["name"] == name {
			return product, true
		}
	}
	return nil, false
}

// runInertia is the scenario for Laravel's React screens.
func runInertia(t *testing.T, fw framework, gw string) {
	admin := newBrowser(t, fw.baseURL, gw)
	name := fmt.Sprintf("Worker test %d", time.Now().UnixNano())

	expectLogin(t, "anonymous list", admin.get(fw.listPath), fw.loginPath)

	_, loginPage := firstVisit(t, admin, fw.loginPath)
	version := loginPage.Version
	failed, _ := admin.inertia(http.MethodPost, fw.loginPath, version, map[string]string{"email": adminEmail, "password": "wrong"})
	expectLogin(t, "wrong password", failed, fw.loginPath)

	loggedIn, _ := admin.inertia(http.MethodPost, fw.loginPath, version, map[string]any{"email": adminEmail, "password": adminPassword, "remember": false})
	if loggedIn.path == fw.loginPath {
		t.Fatalf("login failed: %d %s %.500s", loggedIn.status, loggedIn.path, loggedIn.body)
	}

	list, listPage := admin.inertia(http.MethodGet, fw.listPath, version, nil)
	if list.status != http.StatusOK || listPage.Component != "products/index" {
		t.Fatalf("list after login: %d %s %.300s", list.status, list.path, list.body)
	}

	t.Run("no session leaks to another client", func(t *testing.T) {
		anonymous := newBrowser(t, fw.baseURL, gw)
		guestList := anonymous.get(fw.listPath)
		_, guestLogin := firstVisit(t, newBrowser(t, fw.baseURL, gw), fw.loginPath)
		auth, _ := guestLogin.Props["auth"].(map[string]any)
		if guestList.path != fw.loginPath || (auth != nil && auth["user"] != nil) {
			t.Errorf("leak: anonymous list answered %d %s, guest login page user %v", guestList.status, guestList.path, auth["user"])
		}
	})

	admin.inertia(http.MethodPost, fw.listPath, version, map[string]any{"name": name, "price": "12.34"})
	_, listPage = admin.inertia(http.MethodGet, fw.listPath, version, nil)
	product, ok := productNamed(listPage, name)
	if !ok {
		t.Fatalf("created product not in the list")
	}
	id := fmt.Sprint(product["id"])

	admin.inertia(http.MethodPut, fw.listPath+"/"+id, version, map[string]any{"name": name + " edited", "price": "12.34"})
	_, listPage = admin.inertia(http.MethodGet, fw.listPath, version, nil)
	if _, ok := productNamed(listPage, name+" edited"); !ok {
		t.Fatalf("edited product not in the list")
	}

	admin.inertia(http.MethodDelete, fw.listPath+"/"+id, version, nil)
	_, listPage = admin.inertia(http.MethodGet, fw.listPath, version, nil)
	if _, ok := productNamed(listPage, name+" edited"); ok {
		t.Errorf("deleted product still in the list")
	}

	admin.inertia(http.MethodPost, fw.logoutPath, version, nil)
	expectLogin(t, "list after logout", admin.get(fw.listPath), fw.loginPath)

	measure(t, fw, gw)
}

// runGluoStarter is the scenario for a project fresh from the byjg/gluo starter: its sample
// endpoints, public and behind a JWT, and the users of its test database.
func runGluoStarter(t *testing.T, fw framework, gw string) {
	client := newBrowser(t, fw.baseURL, gw)
	expectStatus := func(what string, p page, status int) {
		t.Helper()
		if p.status != status {
			t.Fatalf("%s: expected %d, got %d %.300s", what, status, p.status, p.body)
		}
	}

	ping, data := client.api(http.MethodGet, "/sample/ping", "", nil)
	expectStatus("public ping", ping, http.StatusOK)
	assertEqual(t, "public ping", data, map[string]any{"result": "pong"})
	anonymous, _ := client.api(http.MethodGet, "/sampleprotected/ping", "", nil)
	expectStatus("protected ping without a token", anonymous, http.StatusUnauthorized)
	failed, _ := client.api(http.MethodPost, fw.loginPath, "", map[string]string{"username": adminEmail, "password": "wrong"})
	expectStatus("wrong password", failed, http.StatusUnauthorized)

	login, data := client.api(http.MethodPost, fw.loginPath, "", map[string]string{"username": adminEmail, "password": adminPassword})
	expectStatus("login", login, http.StatusOK)
	token, _ := data.(map[string]any)["token"].(string)
	if token == "" {
		t.Fatalf("no token in %.300s", login.body)
	}
	protected, _ := client.api(http.MethodGet, "/sampleprotected/ping", token, nil)
	expectStatus("protected ping with the token", protected, http.StatusOK)

	t.Run("no session leaks to another client", func(t *testing.T) {
		guest, _ := newBrowser(t, fw.baseURL, gw).api(http.MethodGet, "/sampleprotected/ping", "", nil)
		if guest.status != http.StatusUnauthorized {
			t.Errorf("protected ping right after an admin request: expected 401, got %d %.300s", guest.status, guest.body)
		}
	})

	missing, _ := client.api(http.MethodGet, "/missing", token, nil)
	expectStatus("unknown route", missing, http.StatusNotFound)

	measurePath(t, fw, "/sampleprotected/ping", func() { client.api(http.MethodGet, "/sampleprotected/ping", token, nil) })
}

// measure logs the average time of an anonymous login page, for docs/compatibility.md.
func measure(t *testing.T, fw framework, gw string) {
	b := newBrowser(t, fw.baseURL, gw)
	measurePath(t, fw, fw.loginPath, func() { b.get(fw.loginPath) })
}

func measurePath(t *testing.T, fw framework, path string, request func()) {
	request()
	const n = 30
	start := time.Now()
	for range n {
		request()
	}
	t.Logf("%s: GET %s average %s over %d requests", fw.boot, path, (time.Since(start) / n).Round(100*time.Microsecond), n)
}

// api sends a JSON request, with a bearer token when there is one.
func (b *browser) api(method, path, token string, body any) (page, any) {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = strings.NewReader(string(data))
	}
	headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	p := b.do(method, path, reader, headers)
	var decoded any
	_ = json.Unmarshal([]byte(p.body), &decoded)
	return p, decoded
}

// runAPI is the scenario for Gluo: a JSON API behind a JWT, which its React SPA calls.
func runAPI(t *testing.T, fw framework, gw string) {
	client := newBrowser(t, fw.baseURL, gw)
	name := fmt.Sprintf("Worker test %d", time.Now().UnixNano())
	expectStatus := func(what string, p page, status int) {
		t.Helper()
		if p.status != status {
			t.Fatalf("%s: expected %d, got %d %.300s", what, status, p.status, p.body)
		}
	}

	anonymous, _ := client.api(http.MethodGet, fw.listPath, "", nil)
	expectStatus("anonymous list", anonymous, http.StatusUnauthorized)
	failed, _ := client.api(http.MethodPost, fw.loginPath, "", map[string]string{"username": adminEmail, "password": "wrong"})
	expectStatus("wrong password", failed, http.StatusUnauthorized)

	login, data := client.api(http.MethodPost, fw.loginPath, "", map[string]string{"username": adminEmail, "password": adminPassword})
	expectStatus("login", login, http.StatusOK)
	token, _ := data.(map[string]any)["token"].(string)
	if token == "" {
		t.Fatalf("no token in %.300s", login.body)
	}

	list, _ := client.api(http.MethodGet, fw.listPath, token, nil)
	expectStatus("list", list, http.StatusOK)

	t.Run("no session leaks to another client", func(t *testing.T) {
		guest, _ := newBrowser(t, fw.baseURL, gw).api(http.MethodGet, fw.listPath, "", nil)
		if guest.status != http.StatusUnauthorized {
			t.Errorf("anonymous list right after an admin request: expected 401, got %d %.300s", guest.status, guest.body)
		}
	})

	created, data := client.api(http.MethodPost, fw.listPath, token, map[string]any{"name": name, "price": 12.34})
	expectStatus("create", created, http.StatusOK)
	id := fmt.Sprint(data.(map[string]any)["id"])

	updated, _ := client.api(http.MethodPut, fw.listPath+"/"+id, token, map[string]any{"name": name + " edited", "price": 12.34})
	expectStatus("update", updated, http.StatusOK)
	product, data := client.api(http.MethodGet, fw.listPath+"/"+id, token, nil)
	expectStatus("get the edited product", product, http.StatusOK)
	if data.(map[string]any)["name"] != name+" edited" {
		t.Errorf("edited product: %.300s", product.body)
	}

	deleted, _ := client.api(http.MethodDelete, fw.listPath+"/"+id, token, nil)
	expectStatus("delete", deleted, http.StatusOK)
	gone, _ := client.api(http.MethodGet, fw.listPath+"/"+id, token, nil)
	expectStatus("deleted product", gone, http.StatusNotFound)

	measurePath(t, fw, fw.listPath, func() { client.api(http.MethodGet, fw.listPath, token, nil) })
}

func TestCompat(t *testing.T) {
	laravelEnv := []string{"DB_HOST=127.0.0.1", "DB_PORT=3307"}
	frameworks := []framework{
		{project: "frameworks/fullstack/symfony-official", boot: "symfony.php", baseURL: "http://localhost:8083", run: runForms,
			loginPath: "/login", listPath: "/product", newPath: "/product/new", logoutPath: "/logout",
			userField: "_username", passwordField: "_password", nameField: "product[name]", priceField: "product[price]"},
		{project: "frameworks/fullstack/cakephp", boot: "cakephp.php", baseURL: "http://localhost:8084", run: runForms,
			env:       []string{"DEBUG=false", "APP_FULL_BASE_URL=http://localhost:8084"},
			loginPath: "/users/login", listPath: "/products", newPath: "/products/add", logoutPath: "/users/logout",
			userField: "email", passwordField: "password", nameField: "name", priceField: "price"},
		{project: "frameworks/fullstack/codeigniter", boot: "codeigniter.php", baseURL: "http://localhost:8085", run: runForms,
			loginPath: "/login", listPath: "/products", newPath: "/products/new", logoutPath: "/logout",
			userField: "email", passwordField: "password", nameField: "name", priceField: "price"},
		{project: "frameworks/gluo", boot: "gluo.php", baseURL: "http://localhost:8080", run: runAPI, env: []string{"APP_ENV=dev"},
			requires:  gluoApplication,
			loginPath: "/login", listPath: "/product"},
		{name: "gluo-starter", project: os.Getenv("COMPAT_GLUO_STARTER"), boot: "gluo.php", baseURL: "http://localhost:8080",
			run: runGluoStarter, env: []string{"APP_ENV=test"}, requires: gluoApplication, loginPath: "/login"},
		{project: "frameworks/fullstack/laravel", boot: "laravel-fresh.php", baseURL: "http://localhost:8081", run: runInertia, env: laravelEnv,
			loginPath: "/login", listPath: "/products", logoutPath: "/logout"},
		{project: "frameworks/fullstack/laravel", boot: "laravel-shared.php", baseURL: "http://localhost:8081", run: runInertia, env: laravelEnv,
			loginPath: "/login", listPath: "/products", logoutPath: "/logout"},
	}
	for _, fw := range frameworks {
		name := cmpOr(fw.name, strings.TrimSuffix(fw.boot, ".php"))
		t.Run(name, func(t *testing.T) {
			if only := os.Getenv("COMPAT_ONLY"); only != "" && !strings.Contains(name, only) {
				t.Skip("COMPAT_ONLY=" + only)
			}
			if fw.project == "" {
				t.Skip("COMPAT_GLUO_STARTER is not set")
			}
			fw.run(t, fw, startCompatGateway(t, fw))
		})
	}
}
