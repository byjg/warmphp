# Getting started

WarmPHP keeps your PHP application booted in memory and serves HTTP with it, using the
plain PHP CLI. You put it behind your usual web server, as you would PHP-FPM.

You need Go to build it, and a PHP 8.1 or newer CLI to run it, from your distribution or any image.
The binary carries its PHP files, so it runs from any folder, on its own.

## Install PHP

WarmPHP uses the PHP you already have: nothing is built for it. `-prefork` also needs the `pcntl`
and `posix` extensions. Without them the gateway refuses `-prefork` when it starts and names what is
missing, for example:

```
-prefork needs PHP's pcntl and posix extensions, and this PHP lacks posix_kill: install them (see Requirements in the README), or run without -prefork
```

The plain workers (no `-prefork`) need neither.

### Ubuntu (apt)

```bash
sudo apt install php-cli          # pcntl and posix are included
```

Your application's extensions are packages too: `php-mysql`, `php-mbstring`, `php-xml`...
Tested on Ubuntu 26.04, PHP 8.5.4.

### Fedora (dnf)

```bash
sudo dnf install php-cli php-process   # php-process brings posix; pcntl is in php-cli
```

Without `php-process`, `-prefork` is refused for `posix_kill`. Tested on Fedora 44, PHP 8.5.12.

### Docker's official image

`php:8.5-cli` has `posix` but not `pcntl`. As it is, it runs the plain workers; for `-prefork`, add
`pcntl` with the image's own tool:

```dockerfile
FROM php:8.5-cli
RUN docker-php-ext-install pcntl
```

Then run PHP through it, as in [PHP in Docker](#7-php-in-docker). Tested with PHP 8.5.12.

### byjg/php images

`byjg/php:8.5-cli` and `byjg/php:8.5-fpm-nginx` have `pcntl` and `posix`. They load Xdebug in
`develop` mode, which slows every request, so turn it off:

```bash
./warmphp \
  -php "docker run --rm -i --init -v $PWD:$PWD -v /tmp:/tmp -u $(id -u):$(id -g) byjg/php:8.5-cli php -d xdebug.mode=off" \
  examples/hello/app.php
```

`make test` runs on `byjg/php:8.5-fpm-nginx` by default, and the benchmark uses it. Tested with PHP 8.5.12.

## 1. Build

```bash
make build
```

This gives `./warmphp`, a static binary for this machine. For another one:
`make build GOOS=linux GOARCH=arm64`.

## 2. Run the example

```bash
./warmphp -workers 4 examples/hello/app.php
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
./warmphp app.php
```

An **old-style `index.php`** that does everything at the top level can run as it is, in script
mode. It is included again on every request; only the loaded classes stay in memory, so it gains
less:

```bash
./warmphp -mode script public/index.php
```

### 3.2 Existing frameworks

A framework's own `index.php` boots it on every request, and most break when it is included twice.
The binary has an entry for each framework instead, from [`compat/`](../compat): it boots the
framework once and handles each request the way the framework's own worker runtime does. Your
application is not changed; `-framework` picks the entry and `-project-root` says where your
application is:

```bash
./warmphp -framework symfony -project-root /path/to/my-symfony-app
```

The `PROJECT_ROOT` variable does the same, if you prefer it; `-project-root` wins over it.

| `-framework`     | Status                                                                                       |
|------------------|----------------------------------------------------------------------------------------------|
| `symfony`        | ✅ one kernel, reset between requests                                                         |
| `cakephp`        | ✅ one application, a new container per request                                              |
| `codeigniter`    | ✅ its own worker mode                                                                        |
| `gluo`           | ✅ one `Application` (byjg/gluo-core 7.0; older Gluo projects are not supported)              |
| `laravel-shared` | ✅ one application, its session, login and array cache reset between requests; for state your application changes during a request (config, locale), see [compatibility.md](compatibility.md) |
| `laravel-fresh`  | ⚠️ builds a new application per request: little gain, and it needs `-max-requests` (e.g. 100) |

Each entry was tested on a real application of that framework, without changing it:
[compatibility.md](compatibility.md) has the results and what each entry works around.

**Another framework, or a change to an entry?** Write the PHP files out and pass the entry you
changed, keeping it beside the entries it requires:

```bash
./warmphp -extract ./warmphp-php
./warmphp -project-root /path/to/app ./warmphp-php/compat/my-framework.php
```

An entry has to do three things: boot the framework once, return a function that builds the
framework's request from the globals (`PhpWorker::input()` for the body) and sends its response,
and reset what the framework keeps per request. Look for the framework's FrankenPHP or RoadRunner
integration: it does the same.

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
./warmphp -listen 127.0.0.1:8080 -workers 4 -prefork -max-requests 1000 -timeout 30s app.php
```

| Flag                 | What it is for                                                                                  |
|----------------------|-------------------------------------------------------------------------------------------------|
| `-workers 4`         | How many requests PHP serves at the same time; about one per CPU core                           |
| `-prefork`           | Boots once and forks the workers: they share OPcache and memory, so they use much less of it    |
| `-max-requests 1000` | Replaces a worker after that many requests, in case your application leaks memory              |
| `-timeout 30s`       | A request that takes longer gets a 504, and its worker is replaced                              |

Turn OPcache on for the CLI (`opcache.enable_cli=1`, in `php.ini` or with `-php "php -d opcache.enable_cli=1"`).

The gateway logs JSON on stderr: a record per request with its time, CPU and memory in PHP, and PHP's
errors, warnings and `error_log()`. An OpenTelemetry Collector can read them; see
[Logs](../README.md#logs).

## 7. PHP in Docker

`-php` is the command that runs PHP. It must keep stdin open (`-i`), and `--init` lets PHP stop
cleanly:

```bash
./warmphp \
  -php "docker run --rm -i --init -v $PWD:$PWD -v /tmp:/tmp -u $(id -u):$(id -g) php:8.5-cli php" \
  examples/hello/app.php
```

PHP must see `-upload-dir` (the system's temp folder by default, hence `-v /tmp:/tmp`): the gateway
writes its PHP files there when it starts. Without that, PHP stops with "Could not open input file".
Environment variables do not reach the container unless you pass them with `-e`; `-project-root`
does, as it is an argument.

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
- [analysis.md](analysis.md): what those numbers mean, and which server to choose
