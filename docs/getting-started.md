# Getting started

phpworker-gateway keeps your PHP application booted in memory and serves HTTP with it, using the
plain PHP CLI. You put it behind your usual web server, as you would PHP-FPM.

You need Go to build it, and PHP 8 CLI to run it (with `pcntl` for `-prefork`).

## 1. Build

```bash
make build
```

This gives `./phpworker-gateway`, a static binary for this machine. For another one:
`make build GOOS=linux GOARCH=arm64`.

## 2. Run the example

```bash
./phpworker-gateway -workers 4 examples/hello/app.php
curl 'localhost:8080/?name=ana'
```

Call it a few times: `requests_served` grows, because the application is not booted again.

## 3. Run your application

The gateway runs an **entry file**: it boots once per worker (once for all of them with
`-prefork`), then handles every request.

### 3.1 Your own code

The entry boots once and returns the function that handles each request:

```php
<?php
// app.php: runs once per worker
require __DIR__ . '/vendor/autoload.php';
$app = buildMyApplication();          // container, routes, configuration...

return static function () use ($app): void {
    $app->handle();                   // runs on every request, with $_GET, $_POST, $_SERVER...
};
```

```bash
./phpworker-gateway app.php
```

An **old-style `index.php`** that does everything at the top level can run as it is, in script
mode. It is included again on every request; only the loaded classes stay in memory, so it gains
less:

```bash
./phpworker-gateway -mode script public/index.php
```

### 3.2 Existing frameworks

A framework's own `index.php` boots it on every request, and most break when it is included twice.
[`compat/`](../compat) has an entry for each framework instead: it boots the framework once and
handles each request the way the framework's own worker runtime does. Your application is not
changed; `PROJECT_ROOT` tells the entry where it is:

```bash
PROJECT_ROOT=/path/to/my-symfony-app ./phpworker-gateway compat/symfony.php
```

| Framework   | Entry                                                    | Status                                                                                       |
|-------------|----------------------------------------------------------|----------------------------------------------------------------------------------------------|
| Symfony     | [`compat/symfony.php`](../compat/symfony.php)            | ✅ one kernel, reset between requests                                                         |
| CakePHP     | [`compat/cakephp.php`](../compat/cakephp.php)            | ✅ one application, a new container per request                                              |
| CodeIgniter | [`compat/codeigniter.php`](../compat/codeigniter.php)    | ✅ its own worker mode                                                                        |
| Gluo 7.0    | [`compat/gluo.php`](../compat/gluo.php)                  | ✅ one `Application` (byjg/gluo-core 7.0; older Gluo projects are not supported)              |
| Laravel     | [`compat/laravel-fresh.php`](../compat/laravel-fresh.php) | ⚠️ works, but builds a new application per request: little gain, and it needs `-max-requests` (e.g. 100) |
|             | [`compat/laravel-shared.php`](../compat/laravel-shared.php) | ❌ fast, but leaks the logged-in user to other clients: do not use it; Laravel needs Octane |

Each entry was tested on a real application of that framework, without changing it:
[compatibility.md](compatibility.md) has the results and what each entry works around.

**Another framework?** Copy the closest entry. It has to do three things: boot the framework once,
return a function that builds the framework's request from the globals (`PhpWorker::input()` for
the body) and sends its response, and reset what the framework keeps per request. Look for the
framework's FrankenPHP or RoadRunner integration: it does the same.

## 4. The one change in your code

`php://input` is empty, because PHP runs as a CLI. Read the raw request body with
`PhpWorker::input()` instead:

```php
$data = json_decode(PhpWorker::input(), true);
```

Everything else works as usual: `$_GET`, `$_POST`, `$_FILES`, `$_COOKIE`, `header()`,
`setcookie()`, `http_response_code()`, sessions, `echo`.

## 5. Put nginx in front

nginx serves your static files and passes the rest to the gateway:

```nginx
upstream php {
    server 127.0.0.1:8080;
    keepalive 16;
}

server {
    listen 80;
    root /path/to/my-app/public;

    location / {
        try_files $uri @php;
    }

    location @php {
        proxy_pass http://php;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

With Caddy it is one line: `reverse_proxy localhost:8080`.

## 6. Production settings

```bash
./phpworker-gateway -listen 127.0.0.1:8080 -workers 4 -prefork -max-requests 1000 -timeout 30s app.php
```

| Flag                 | What it is for                                                                                  |
|----------------------|-------------------------------------------------------------------------------------------------|
| `-workers 4`         | How many requests PHP serves at the same time; about one per CPU core                           |
| `-prefork`           | Boots once and forks the workers: they share OPcache and memory, so they use much less of it    |
| `-max-requests 1000` | Replaces a worker after that many requests, in case your application leaks memory              |
| `-timeout 30s`       | A request that takes longer gets a 504, and its worker is replaced                              |

Turn OPcache on for the CLI (`opcache.enable_cli=1`, in `php.ini` or with `-php "php -d opcache.enable_cli=1"`).

## 7. PHP in Docker

`-php` is the command that runs PHP. It must keep stdin open (`-i`), and `--init` lets PHP stop
cleanly:

```bash
./phpworker-gateway \
  -php "docker run --rm -i --init -v $PWD:$PWD -v /tmp:/tmp -u $(id -u):$(id -g) byjg/php:8.5-cli php" \
  examples/hello/app.php
```

## What to watch for

The application stays in memory between requests, so:

- **Do not keep request data in static properties or global variables.** The next request, maybe
  another user's, sees it.
- **With `-prefork`, open database and cache connections on the first request, not at boot**:
  a connection opened at boot would be shared by every worker.
- **A fatal error or `exit()` is fine**: the request is answered and the worker is replaced.

## Next

- [README](../README.md): every flag, the protocol, how the web functions are redefined
- [compatibility.md](compatibility.md): how each framework runs, and what was found
- [benchmark.md](benchmark.md): FPM, the gateway and FrankenPHP compared on a real journey
