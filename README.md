# WarmPHP

[![Build Status](https://github.com/byjg/warmphp/actions/workflows/build.yml/badge.svg?branch=master)](https://github.com/byjg/warmphp/actions/workflows/build.yml)
[![Opensource ByJG](https://img.shields.io/badge/opensource-byjg-success.svg)](http://opensource.byjg.com)
[![Install MCP Server](https://img.shields.io/badge/Install-MCP_Server-8A2BE2?logo=modelcontextprotocol&logoColor=white)](https://opensource.byjg.com/docs/ai/mcpserver-byjg-docs/)
[![GitHub source](https://img.shields.io/badge/Github-source-informational?logo=github)](https://github.com/byjg/warmphp/)
[![GitHub license](https://img.shields.io/github/license/byjg/warmphp.svg)](https://opensource.byjg.com/license/)
[![GitHub release](https://img.shields.io/github/release/byjg/warmphp.svg)](https://github.com/byjg/warmphp/releases/)

**WarmPHP turns your PHP CLI into a web application server.**

A pool of persistent workers, structured JSON logs and automatic worker recovery, all using your
existing PHP binary.

No FPM, no custom SAPI, no custom extension. Your application boots once and stays warm.

```text
Caddy / HAProxy / nginx / static-httpserver
              │  plain HTTP (reverse proxy)
              ▼
      WarmPHP (Go)
        - parses HTTP, builds $_SERVER (CGI/1.1 variables)
        - keeps N workers alive, one request per worker at a time
        - restarts crashed workers, kills slow ones, recycles after max-requests
              │  stdin/stdout: length-prefixed JSON frames, bodies as raw bytes
   ┌──────────┼──────────┐
 php worker.php   php worker.php   php worker.php   ← boot once, stay in memory
```

## Documentation

- [Getting started](docs/getting-started.md): installation, applications and deployment.
- [Framework compatibility](docs/compatibility.md): tested integrations and limitations.
- [Benchmark](docs/benchmark.md) and [analysis](docs/analysis.md): measurements and how to interpret them.
- [Log shipping](docs/logging.md) and [worker protocol](docs/protocol.md): operational and implementation references.

## Requirements

Any PHP 8.1 or newer CLI: a distribution's package or any image, nothing built for WarmPHP. `-mode prefork`
also needs the `pcntl` and `posix` extensions; `php_memory_kb` in the logs needs PHP 8.2. Go only to
build it.

Without `pcntl` or `posix`, the gateway refuses `-mode prefork` when it starts and names what is missing.
The gateway's tests pass with:

| PHP | Version | For `-mode prefork` |
|-----|---------|----------------|
| Ubuntu 26.04, `apt install php-cli` | 8.5.4 | nothing more |
| Fedora 44, `dnf install php-cli` | 8.5.12 | `dnf install php-process` (`posix`) |
| Docker's official `php:8.5-cli` | 8.5.12 | `docker-php-ext-install pcntl` |
| `byjg/php:8.5-cli`, `byjg/php:8.5-fpm-nginx` | 8.5.12 | nothing more; turn Xdebug off with `-d xdebug.mode=off` |

How to install each one: [Install PHP](docs/getting-started.md#install-php).

## Install

One static binary, for Linux and macOS, on amd64 and arm64. It needs no PHP to install: it runs the
PHP CLI you point it to.

### Linux Packages (Debian/Fedora/Alpine)

Add the [ByJG package repository](https://opensource.byjg.com/docs/packages#setup) for your distribution, then:

```bash
sudo apt install warmphp   # Debian/Ubuntu
sudo dnf install warmphp   # Fedora/RHEL
sudo apk add warmphp       # Alpine
```

Alpine splits PHP into small packages: `php84` alone runs WarmPHP; add the extensions your application
needs beside it (`php84-session` for sessions, `php84-pdo_mysql`...).

### Homebrew

On macOS (or Linux) with [Homebrew](https://brew.sh):

```bash
brew install byjg/tap/warmphp
```

Homebrew builds it from source, installing Go only for the build.

### From source

```bash
make build      # ./warmphp
```

Any other platform Go supports: `make build GOOS=darwin GOARCH=arm64`. `warmphp -version` says which
version you have.

## Quick start

New here? [docs/getting-started.md](docs/getting-started.md) covers what most people need: install,
run your application or framework, put nginx in front, production settings.

```bash
make build
./warmphp -workers 4 examples/hello/app.php
curl 'localhost:8080/?name=joao'   # requests_served keeps growing: the app is not rebooted
```

`make build` builds a static binary (no cgo), which runs on any Linux, Alpine included. `GOOS` and
`GOARCH` build it for another platform, e.g. `make build GOOS=darwin GOARCH=arm64`.

Put any reverse proxy in front of it, e.g. Caddy: `reverse_proxy localhost:8080`.

## More than `php -S`

PHP's built-in development server starts your application afresh for each request. WarmPHP keeps it
loaded across requests, manages multiple workers, and logs request timing, worker activity and PHP
errors.

## Modes

Every mode runs **persistent `php` CLI processes** (no threads): a worker serves one request at a
time and lives until it is replaced. `-mode` chooses what the entry file is and how the workers start:

| `-mode`            | Entry file                                                         | How the workers start                                                    | What stays in memory between requests                                    |
|--------------------|--------------------------------------------------------------------|--------------------------------------------------------------------------|--------------------------------------------------------------------------|
| `worker` (default) | Runs **once** and **returns a callable**, which handles each request | Each one is a `php` process of its own, which boots the application    | Everything the entry built: container, routes, connections, caches      |
| `prefork`          | The same as `worker`                                               | One master boots the application once and **forks** them ([Pre-fork](#pre-fork)) | The same as `worker`; the workers share the master's memory and OPcache |
| `legacy`           | A **front controller** (`index.php`), included **on every request**  | As `worker`                                                              | Every class, function and constant loaded, every `require_once` file, static properties, `$GLOBALS` |

```php
<?php
// -mode worker and -mode prefork: the entry returns the request handler
$app = buildMyApplication();           // once per worker
return static fn() => $app->handle();  // every request
```

### Coming from FrankenPHP or RoadRunner

| You know                          | In WarmPHP                                                                        |
|-----------------------------------|-----------------------------------------------------------------------------------|
| FrankenPHP worker mode, RoadRunner | `-mode worker`: the entry returns a callable instead of looping over `frankenphp_handle_request()` |
| PHP-FPM's pre-fork of the children | `-mode prefork`                                                                   |
| FrankenPHP classic mode, PHP-FPM   | **No equivalent.** They start each request from nothing; no WarmPHP mode does. `-mode legacy` looks like them, but its process stays loaded (below) |
| FrankenPHP's `watch`               | `-watch` ([Development](#development--watch))                                    |

### When to use each mode

| Situation                                                                 | Use                                   | Why                                                                  |
|---------------------------------------------------------------------------|---------------------------------------|----------------------------------------------------------------------|
| A framework of [`compat/`](https://github.com/byjg/warmphp/tree/master/compat) (`-framework`), or an entry that returns a callable | `-mode worker`                 | The default: boots once per worker, no `pcntl` needed                |
| The same in production, when memory counts or there are many workers      | `-mode prefork`                       | One boot, shared OPcache and memory (Gluo, 4 workers: 59 MiB prefork vs 108 MiB worker, lower is better; [measurement scope](docs/benchmark.md#memory-mib-lower-is-better)) |
| A PHP without `pcntl` and `posix` (Docker's `php:*-cli`, Fedora without `php-process`) | `-mode worker`             | `prefork` needs them                                                 |
| Development, editing the code                                             | `-mode worker -watch` (or `legacy -watch`) | The next request after a save runs the new code; not with `prefork` |
| An application that only has a front controller, and is not rewritten yet | `-mode legacy`                        | Runs it as it is; read the rules below first                         |
| An application that must start every request from nothing                 | Not WarmPHP: PHP-FPM or FrankenPHP classic | Every WarmPHP mode keeps the process, and what it loaded, between requests |

### `-mode legacy`: a front controller in a persistent process

`-mode legacy` includes the entry file again on every request, as a web server runs `index.php`. It
is **not** a fresh start per request, as PHP-FPM or FrankenPHP's classic mode are: the worker
process stays, and so does everything PHP loaded in it.

| What                                                          | On the next request                                            |
|---------------------------------------------------------------|----------------------------------------------------------------|
| The entry file itself                                         | Runs again, read again                                         |
| Files the entry loads with `require` / `include`              | Run again                                                      |
| Files loaded with `require_once` / `include_once`             | **Skipped**: `require_once` returns `true`, and an edit to them is not seen |
| Classes (autoloaded or not), functions, constants             | **Kept**, from the first request. Declaring them again fails: "Cannot redeclare" |
| Static properties, `$GLOBALS`, singletons                     | **Kept**: state one request leaves there is seen by the next   |
| Local variables, objects the entry creates                    | Rebuilt                                                        |

So, in a front controller run with `-mode legacy`:

- Declare functions and classes in files loaded with `require_once` (or the autoloader), never in the
  entry file: the second request fails with "Cannot redeclare".
- Use `require_once` for what defines things, `require` for what must run on every request.
- Do not keep per-request state in static properties, globals or singletons, or reset it at the
  start of the entry.
- `define()` in the entry warns on the second request ("already defined"): guard it with
  `defined()`.
- An entry that calls `exit()` after each response (Symfony's runtime, CodeIgniter) works, but the
  worker is replaced every time: nothing stays warm. [docs/compatibility.md](docs/compatibility.md)
  lists what each framework's `public/index.php` does in `-mode legacy`; the frameworks there run in
  `-mode worker` through their boot files instead.

[`examples/legacy`](https://github.com/byjg/warmphp/tree/master/examples/legacy) is a front controller written for it:

```bash
./warmphp -mode legacy examples/legacy/index.php
curl 'localhost:8080/?name=joao'
```

### Development: `-watch`

A worker keeps the code it booted with (in `-mode legacy`, the files it loaded): an edit is not seen
until the worker is replaced. `-watch` replaces them when a file of the application changes:

```bash
./warmphp -mode worker -watch -workers 1 public/app.php
```

- It watches the folder given by `-project-root` (or `PROJECT_ROOT`), otherwise the nearest folder
  up from the entry with a `composer.json`. It polls the `.php`, `.env`, `.json`, `.yaml`/`.yml`,
  `.ini`, `.xml` and `.neon` files every 500 ms, and `vendor/composer/installed.json` for `composer
  install` and `update`.
- It skips `.git`, `.idea`, `.vscode`, `node_modules`, `vendor` and the folders frameworks write to
  while serving: `var`, `storage`, `writable`, `tmp`, `cache`, `logs`. A write there would replace
  the workers on every request.
- After a change, **the next request runs the new code**: idle workers are replaced at once, a busy
  one when its request ends, and a worker started before the change is never handed a request (the
  request waits for the new one to boot).
- The log says what changed, and when every worker runs the new code (the paths are relative to
  the watched folder; up to 5 are named, then counted):

  ```
  {"level":"INFO","msg":"code changed, replacing the workers","files":2,"changed":"src/Model/User.php, config/dev/config.php"}
  {"level":"INFO","msg":"reloaded: every worker runs the new code","changed":"src/Model/User.php, config/dev/config.php","workers":4,"ms":262.4}
  ```

  `ms` is from the change to the last worker up, so it includes a request that was running then.
- Code that does not boot (a typo) is retried every second until it does: each try logs PHP's error
  and a warning, and there is no `reloaded` until it boots.
- Not with `-mode prefork`: the gateway refuses it. For development only: in production a worker is
  replaced by a deploy, not by a file that changes.

## What the worker provides on each request

`$_SERVER` (with the environment variables, as in the CLI), `$_GET`, `$_POST` (urlencoded and multipart, including `a[b]` and `a[]`),
`$_COOKIE`, `$_FILES` (in PHP's layout), `$_REQUEST`, and `exit()` and fatal errors (the request
is still answered, then the worker is replaced). All of these are reset between requests.

**Output** works as in a web SAPI: whatever the application writes is the body, streamed to the
gateway in 64 KB chunks, whatever it does with output buffers. Closing them all, as CodeIgniter's
`app/Config/Events.php` does on every request, is fine. The body never goes through PHP's own
output layer, because in the CLI the first byte that does marks the headers as sent for the rest of
the process (`session_start()` and the `session.*` settings then refuse to work). If an application
closes every buffer and writes before opening a new one, its output still reaches the client in
order, and the worker is replaced after that response.

## Web functions work as usual

The CLI SAPI drops response headers. PHP 8 allows redeclaring a function listed in
`disable_functions`, so the gateway starts every worker with these functions disabled (merged
with `php.ini`'s own list) and [`php/functions.php`](https://github.com/byjg/warmphp/blob/master/php/functions.php) redefines them in plain PHP:

| Function                                                           | Behaviour in the worker                                                                                    |
|--------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------|
| `header()`, `header_remove()`, `headers_list()`                    | Same rules as PHP: `$replace`, `$response_code`, `HTTP/1.1 404` and `Status:` lines, `Location` → 302, new lines rejected |
| `http_response_code()`                                             | Reads and sets the response status                                                                         |
| `setcookie()`, `setrawcookie()`                                    | Same `Set-Cookie` lines as PHP, including the options array, `SameSite` and `Partitioned`                  |
| `is_uploaded_file()`, `move_uploaded_file()`                       | Only accept the files uploaded in the current request                                                      |

They work from any code, including namespaced libraries calling `header()` unqualified.
`headers_sent()` keeps PHP's own: it is `false` as long as nothing reaches PHP's output layer.
When the application sets no `Content-Type`, the worker sends `default_mimetype` like the web SAPI.

**Sessions** work with the regular `session_*()` functions. Before each request the worker sets
the id from the session cookie. After it, the worker saves the session and sends what
`session_start()` sends in the web SAPI: the cookie for a new or regenerated id, placed before the
next header the application sets (where PHP would have sent it), and the cache limiter headers
(`Last-Modified` is not sent). The session name is read when the request starts,
so set it at boot or in `php.ini`, not with `session_name()` during the request.

## What the application must change

| Web SAPI      | In the worker                                                                  |
|---------------|--------------------------------------------------------------------------------|
| `php://input` | `PhpWorker::input()`: it is a stream, not a function, so it cannot be redefined |

The application is also responsible for not keeping per-request state in static properties
or in objects built at boot.

## ByJG RestServer

RestServer (7.0) runs with its default `HttpWriter`, cookies included.
[`examples/restserver/app.php`](https://github.com/byjg/warmphp/blob/master/examples/restserver/app.php) hands each request to a new `Server`
with `Server::withRequestObject()`: a PSR-7 request built from the worker's globals and
`PhpWorker::input()`:

```bash
(cd examples/restserver && composer install)
./warmphp examples/restserver/app.php
curl 'localhost:8080/hello?name=joao'
```

## Existing frameworks

Symfony, CakePHP, Laravel, CodeIgniter and Gluo (7.0, through gluo-core's `Application`) were tested without changing them, through the boot files in
[`compat/`](https://github.com/byjg/warmphp/tree/master/compat), which the binary carries:

```bash
./warmphp -framework symfony -project-root /path/to/my-symfony-app
```

[docs/compatibility.md](docs/compatibility.md) lists what worked, what each framework needs and the
leaks found.

## Benchmark

[docs/benchmark.md](docs/benchmark.md) runs the same journey (log in, page through the products, read
one, edit it, add one, page again) on each framework, served by nginx with PHP-FPM, by nginx with the
gateway (`-mode worker` and `-mode prefork`) and by FrankenPHP (classic and worker mode), with OPcache on and off.
`make benchmark` runs it; it takes hours, and its `BENCH_*` variables select what runs, e.g.
`make benchmark BENCH_ONLY=gluo BENCH_JOURNEYS=10`.

[docs/analysis.md](docs/analysis.md) has the conclusions: the strengths and weaknesses of each server,
and the best choice for each framework.

## Options

| Flag             | Default          | Description                                                          |
|------------------|------------------|----------------------------------------------------------------------|
| `-listen`        | `:8080`          | Address to listen on                                                 |
| `-php`           | `php`            | Command that runs the PHP CLI; may include arguments                 |
| `-worker`        | embedded         | Path to a worker script to use instead of the embedded one           |
| `-framework`     |                  | Runs the embedded entry of this framework, instead of an entry file: `cakephp`, `codeigniter`, `gluo`, `laravel-fresh`, `laravel-shared` or `symfony` |
| `-project-root`  |                  | The application's folder, given to PHP as `PROJECT_ROOT`; overrides that variable |
| `-extract`       |                  | Writes the embedded PHP files to this folder, which must be empty, and exits |
| `-mode`          | `worker`         | `worker`, `prefork` or `legacy` ([Modes](#modes)); `-framework` runs in `worker` or `prefork` |
| `-workers`       | number of CPUs   | Number of PHP processes                                              |
| `-max-requests`  | `0`              | Replace a worker after this many requests (0 = never)                |
| `-watch`         | off              | Development: replace the workers when a file of the application changes ([`-watch`](#development--watch)); not with `-mode prefork` |
| `-timeout`       | `30s`            | Maximum time to wait for a worker and process a request (then 504 and the worker is killed) |
| `-boot-timeout`  | `30s`            | Maximum time for a worker to boot                                    |
| `-max-body`      | `32MiB`          | Maximum request body (413 above it)                                  |
| `-upload-dir`    | system temp dir  | Where uploaded files and the embedded PHP files are written; PHP must see the same path |
| `-version`       |                  | Prints the version and exits                                         |

Running PHP in Docker works too. Use `--init`, so PHP is not PID 1 and stops on SIGTERM, and use `-i`
for stdin:

```bash
./warmphp \
  -php "docker run --rm -i --init -v $PWD:$PWD -v /tmp:/tmp -u $(id -u):$(id -g) php:8.5-cli php" \
  examples/hello/app.php
```

## Embedded files

The binary carries `php/` and `compat/`, so it needs no file beside it. PHP can only run real files,
so the gateway writes them to a new folder with a random name inside `-upload-dir`, puts back any that
are missing before it starts a PHP process (a `/tmp` cleaner may delete them from a gateway that runs
for weeks), and removes the folder when it stops. A gateway that is killed leaves it behind.

`-worker` and an entry file take the place of the embedded ones, and `-extract` writes them out to be
changed. After changing `php/` or `compat/` in this repository, run `make build` or pass the files.

## Logs

The gateway logs JSON on stderr, one record per line: its own events, a record per request, and what
PHP writes to its stderr. Errors, warnings and `error_log()` become records of their own, with the
stack trace in one record and the level taken from PHP's prefix:

```json
{"time":"2026-11-02T16:57:38.77Z","level":"INFO","msg":"request","request_id":"8526147b","method":"GET","path":"/products","status":200,"duration_ms":24.6,"queue_ms":0.1,"bytes":812,"worker_pid":28,"php_ms":23.9,"php_cpu_ms":6.2,"php_memory_kb":2840,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"}
{"time":"2026-11-02T16:57:39.36Z","level":"ERROR","msg":"PHP Fatal error:  Uncaught RuntimeException: payment failed in /app/src/Pay.php:42","source":"php","request_id":"a426d85e","worker_pid":28,"stack":"Stack trace:\n#0 /app/src/Checkout.php(18): Pay->charge()\n#1 {main}"}
```

| Field                | Meaning                                                                            |
|----------------------|------------------------------------------------------------------------------------|
| `path`               | Without the query string, which often carries tokens and personal data             |
| `duration_ms`        | Elapsed milliseconds for the whole request in the gateway, including queue time; lower is better for the same workload                                                  |
| `queue_ms`           | Milliseconds waiting for a free worker; lower is better. Sustained growth suggests worker saturation; investigate slow requests and capacity       |
| `php_ms`             | Elapsed milliseconds in PHP; lower is better for the same workload. A timeout or killed worker has none of the `php_*` fields    |
| `php_cpu_ms`         | CPU time in milliseconds; lower means less CPU work for the same request. `php_ms` far above it suggests waiting, often for the database        |
| `php_memory_kb`      | Incremental peak PHP heap in **KiB (1,024 bytes)** despite the `_kb` name; above the request-start heap, excluding OPcache. Lower is better for the same workload; needs PHP 8.2 |
| `bytes` | Response body size in bytes; neither higher nor lower is inherently better |
| `trace_id`           | From the `traceparent` header, when the request has one; PHP sees that header too  |

Heap peak is not total process or container memory, and does not measure retained growth between
requests. Compare it only across equivalent requests. One MiB is 1,024 KiB (1,048,576 bytes).

- `request_id` ties PHP's lines to the request. A worker of its own has its own stderr, so they are
  always tied. With `-mode prefork` all the workers share one, so a line gets the id only when one request
  is running: under load many have none. It is never another request's id.
- Only what reaches stderr is captured. An application that logs to a file keeps doing so; Laravel
  logs to stderr with `LOG_CHANNEL=stderr`. Errors a framework catches go to its logger.
- Records are written in the background, so a destination that stalls or goes away never reaches the
  requests or PHP. While 8,192 records wait, new ones are dropped, and a `logs dropped` record says how
  many (`count`) when writing resumes. A stop waits up to 2 s to write what is queued; `kill -9` or a
  crash loses it.
- A record costs time on the simplest endpoints: see [the benchmark's note](docs/benchmark.md#results).

### Sending the logs somewhere

See [Sending logs to a backend](docs/logging.md) for Fluent Bit, Docker and OpenTelemetry Collector configurations.

## Pre-fork

With `-mode worker`, every worker is a `php` process of its own: it boots the application, and the
CLI gives it its own OPcache. With `-mode prefork`, one master process boots the application and forks
the workers with `pcntl_fork()`, as PHP-FPM forks its children: they share the master's OPcache, and
its memory until they write to it. In the [benchmark](docs/benchmark.md#memory-mib-lower-is-better),
with OPcache on and four PHP workers, Gluo's end-of-run container memory (including nginx) falls
from 108 MiB in worker mode to 59 MiB in prefork mode, about 45 % less. This is not a peak measurement.

- Each forked worker connects to a Unix socket the gateway listens on, in `-upload-dir` (PHP must
  see it), and speaks the same protocol there. It waits on it for its next request with no time
  limit, whatever `default_socket_timeout` says; the application's own sockets keep that setting.
- A forked worker that ended while idle is only seen to end when the gateway writes the next request
  to it: PHP never got that request, so another worker answers it.
- The master replaces a worker that exits (`exit()`, a fatal error, `-max-requests`) with a new fork
  of the booted application, so a replacement needs no boot. A worker past `-timeout` is killed by
  the master, which the gateway asks on the master's stdin.
- Connections opened during the boot (database, cache) would be shared by every worker: open them
  lazily, on the first request. Each worker reseeds `mt_rand()`.
- If the master itself exits, the gateway stops: restart it.

## Protocol

See the [worker protocol reference](docs/protocol.md) for startup, framing and request correlation.

## Environment variables

| Variable              | Read by                     | Default                         |                                                                    |
|-----------------------|-----------------------------|---------------------------------|--------------------------------------------------------------------|
| `PROJECT_ROOT`        | the entries of `compat/`    |                                 | the framework application's folder, when `-project-root` is not given (see [Existing frameworks](#existing-frameworks)) |
| `WARMPHP_PHP`       | `make test`                 | the Makefile's Docker command   | the PHP command of the tests; it must pass stdin through          |
| `WARMPHP_COMPOSER`  | `make test`                 | the Makefile's Docker command   | the Composer command that installs the RestServer example's dependencies |
| `COMPAT_DIR`          | the compatibility tests     | `../gluo-framework-comparison`  | the comparison repository                                          |
| `COMPAT_ONLY`         | the compatibility tests     |                                 | only the builds whose name contains it                             |
| `COMPAT_GLUO_STARTER` | the compatibility tests     |                                 | a project created from the Gluo starter, for the `gluo-starter` case |
| `WARMPHP_IMAGE`     | the compatibility tests     | `byjg/php:8.5-cli-load`         | the PHP image the workers run in                                   |

The benchmark's variables are in [docs/benchmark.md](docs/benchmark.md#reproducing).

## Tests

The tests run real PHP workers:

```bash
make test
```

`WARMPHP_PHP` sets the PHP command, which must pass stdin through. The Makefile's default runs
`byjg/php:8.5-fpm-nginx` in Docker, with Xdebug off (the image loads it):

```bash
make test WARMPHP_PHP="php"   # PHP installed on the machine
```

The tests also run the [RestServer example](#byjg-restserver): `make test` installs its dependencies
with `WARMPHP_COMPOSER` the first run, and again when its `composer.json` changes.

----
[Open source ByJG](http://opensource.byjg.com)
