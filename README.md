# WarmPHP

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

## Requirements

Any PHP 8.1 or newer CLI: a distribution's package or any image, nothing built for WarmPHP. `-prefork`
also needs the `pcntl` and `posix` extensions; `php_memory_kb` in the logs needs PHP 8.2. Go only to
build it.

Without `pcntl` or `posix`, the gateway refuses `-prefork` when it starts and names what is missing.
The gateway's tests pass with:

| PHP | Version | For `-prefork` |
|-----|---------|----------------|
| Ubuntu 26.04, `apt install php-cli` | 8.5.4 | nothing more |
| Fedora 44, `dnf install php-cli` | 8.5.12 | `dnf install php-process` (`posix`) |
| Docker's official `php:8.5-cli` | 8.5.12 | `docker-php-ext-install pcntl` |
| `byjg/php:8.5-cli`, `byjg/php:8.5-fpm-nginx` | 8.5.12 | nothing more; turn Xdebug off with `-d xdebug.mode=off` |

How to install each one: [Install PHP](docs/getting-started.md#install-php).

## Quick start

New here? [docs/getting-started.md](docs/getting-started.md) covers what most people need: build,
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

| Mode                 | Entry file                                                    | What stays in memory                                          |
|----------------------|---------------------------------------------------------------|---------------------------------------------------------------|
| `callable` (default) | Runs once and **returns a callable**, which handles each request | Everything: container, routes, connections, caches            |
| `script`             | Included again on every request (front controller)            | Loaded classes, static properties, `$GLOBALS`; objects are rebuilt |

```php
<?php
// callable mode
$app = buildMyApplication();           // once per worker
return static fn() => $app->handle();  // every request
```

In `script` mode, declare functions and classes in files loaded with `require_once`.
Declaring them in the entry file fails with "Cannot redeclare" on the second request.

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
with `php.ini`'s own list) and [`php/functions.php`](php/functions.php) redefines them in plain PHP:

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
[`examples/restserver/app.php`](examples/restserver/app.php) hands each request to a new `Server`
with `Server::withRequestObject()`: a PSR-7 request built from the worker's globals and
`PhpWorker::input()`:

```bash
(cd examples/restserver && composer install)
./warmphp examples/restserver/app.php
curl 'localhost:8080/hello?name=joao'
```

## Existing frameworks

Symfony, CakePHP, Laravel, CodeIgniter and Gluo (7.0, through gluo-core's `Application`) were tested without changing them, through the boot files in
[`compat/`](compat), which the binary carries:

```bash
./warmphp -framework symfony -project-root /path/to/my-symfony-app
```

[docs/compatibility.md](docs/compatibility.md) lists what worked, what each framework needs and the
leaks found.

## Benchmark

[docs/benchmark.md](docs/benchmark.md) runs the same journey (log in, page through the products, read
one, edit it, add one, page again) on each framework, served by nginx with PHP-FPM, by nginx with the
gateway (with and without `-prefork`) and by FrankenPHP (classic and worker mode), with OPcache on and off.
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
| `-mode`          | `callable`       | `callable` or `script`                                               |
| `-workers`       | number of CPUs   | Number of PHP processes                                              |
| `-max-requests`  | `0`              | Replace a worker after this many requests (0 = never)                |
| `-timeout`       | `30s`            | Maximum time to wait for a worker and process a request (then 504 and the worker is killed) |
| `-boot-timeout`  | `30s`            | Maximum time for a worker to boot                                    |
| `-max-body`      | `32MiB`          | Maximum request body (413 above it)                                  |
| `-upload-dir`    | system temp dir  | Where uploaded files and the embedded PHP files are written; PHP must see the same path |
| `-prefork`       | off              | Boot once in a master that forks the workers (see below); needs `pcntl` and `posix` |

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
| `duration_ms`        | The whole request, in the gateway                                                  |
| `queue_ms`           | Waiting for a free worker: when it grows, the application needs more workers       |
| `php_ms`             | The request in PHP. A timeout or a killed worker has none of the `php_*` fields    |
| `php_cpu_ms`         | CPU PHP used: `php_ms` far above it means waiting, usually for the database        |
| `php_memory_kb`      | Peak of PHP's heap above what the worker already held, without OPcache; needs PHP 8.2 |
| `trace_id`           | From the `traceparent` header, when the request has one; PHP sees that header too  |

- `request_id` ties PHP's lines to the request. A worker of its own has its own stderr, so they are
  always tied. With `-prefork` all the workers share one, so a line gets the id only when one request
  is running: under load many have none. It is never another request's id.
- Only what reaches stderr is captured. An application that logs to a file keeps doing so; Laravel
  logs to stderr with `LOG_CHANNEL=stderr`. Errors a framework catches go to its logger.
- Records are written in the background, so a destination that stalls or goes away never reaches the
  requests or PHP. While 8,192 records wait, new ones are dropped, and a `logs dropped` record says how
  many (`count`) when writing resumes. A stop waits up to 2 s to write what is queued; `kill -9` or a
  crash loses it.
- A record costs time on the simplest endpoints: see [the benchmark's note](docs/benchmark.md#results).

### Sending the logs somewhere

The gateway keeps no log file: it writes to stderr, and these are the ways to take the logs from there.

**[Fluent Bit](https://fluentbit.io/), through a pipe.** If it exits, the gateway keeps serving, but
the logs stop until both are restarted:

```bash
./warmphp app.php 2>&1 >/dev/null | fluent-bit -i stdin -o stdout
```

**Docker, with its `fluentd` log driver.** It sends the container's stream to Fluent Bit, wrapping
each record in a `log` field. With `fluentd-async=true`, the container starts even when Fluent Bit
is down:

```bash
docker run --log-driver fluentd --log-opt fluentd-address=127.0.0.1:24224 \
  --log-opt fluentd-async=true --log-opt tag=warmphp ...
```

Fluent Bit opens the `log` field with a parser that knows the gateway's time format. In a file, here
`warmphp.conf`:

```ini
[PARSER]
    Name        warmphp
    Format      json
    Time_Key    time
    Time_Format %Y-%m-%dT%H:%M:%S.%L%z
```

```bash
fluent-bit -R warmphp.conf -i forward -p port=24224 \
  -F parser -m '*' -p key_name=log -p parser=warmphp -o stdout -m '*'
```

Replace `-o stdout` with the output of your backend.

**A file, read by the OpenTelemetry Collector.** Redirect stderr to it, and rotate it with `logrotate`
and `copytruncate`, as nothing else does:

```bash
./warmphp app.php 2>> /logs/gateway.log
```

The [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) reads the records as logs.
This configuration of its contrib distribution (tested on 0.164) sets the time, the severity, the body
and the trace id. `include` is the path as the Collector sees it:

```yaml
receivers:
  file_log:
    include: [/logs/gateway.log]
    operators:
      - type: json_parser
        timestamp:
          parse_from: attributes.time
          layout_type: gotime
          layout: 2006-01-02T15:04:05.999999999Z07:00
        severity:
          parse_from: attributes.level
      - type: move
        from: attributes.msg
        to: body
      - type: trace_parser
        if: attributes.trace_id != nil
        trace_id:
          parse_from: attributes.trace_id
exporters:
  debug:
service:
  pipelines:
    logs:
      receivers: [file_log]
      exporters: [debug]
```

Replace `debug` with the exporter of your backend.

## Pre-fork

Without `-prefork`, every worker is a `php` process of its own: it boots the application, and the
CLI gives it its own OPcache. With `-prefork`, one master process boots the application and forks
the workers with `pcntl_fork()`, as PHP-FPM forks its children: they share the master's OPcache, and
its memory until they write to it. With 4 workers, Gluo's container goes from 108 to 59 MiB.

- Each forked worker connects to a Unix socket the gateway listens on, in `-upload-dir` (PHP must
  see it), and speaks the same protocol there.
- The master replaces a worker that exits (`exit()`, a fatal error, `-max-requests`) with a new fork
  of the booted application, so a replacement needs no boot. A worker past `-timeout` is killed by
  the master, which the gateway asks on the master's stdin.
- Connections opened during the boot (database, cache) would be shared by every worker: open them
  lazily, on the first request. Each worker reseeds `mt_rand()`.
- If the master itself exits, the gateway stops: restart it.

## Protocol

Workers start as `php -d disable_functions=<list> worker.php <mode> <entry>`; the gateway gets
`<list>` once from `php worker.php probe`, a JSON object that also names what `-prefork` needs and
this PHP lacks (`pcntl_fork`, `pcntl_waitpid`, `posix_kill`). With `-prefork`, the master starts as
`php -d disable_functions=<list> worker.php <mode> <entry> <socket> <workers>`, and each forked
worker speaks the protocol on its connection to `<socket>` instead of stdin and stdout. With
`-project-root`, `--project-root=<dir>` comes last, and the worker sets `PROJECT_ROOT` before it
loads the entry.

A frame is a 4-byte big-endian length followed by JSON. After booting, the worker sends the frame
`{"ready":true,"pid":<pid>}`. Then, for each request, the gateway sends a frame on stdin, with a new
random boundary, followed by the raw request body, and the worker answers on stdout with the raw
body, the boundary, and a frame:

```text
gateway → worker   {"boundary":"--warmphp-<random>","id":"<request id>","server":{...CGI vars...},"body_size":<n>,"fields":[[name,value]],"files":[{field,name,type,tmp_name,error,size}]}<n body bytes>
worker  → gateway  <body bytes>--warmphp-<random>{"status":200,"headers":["Content-Type: text/html"],"metrics":{"ms":23.9,"cpu_ms":6.2,"memory_kb":2840},"exit":false}
```

Output outside a request, such as boot messages, goes to stderr, so it never mixes with a body.
Around each request the worker writes `\x01start <id> <pid>` and `\x01end <id>` to stderr, each on a
line of its own: they come through the pipe in order with PHP's own lines, which is how the gateway
ties those lines to the request.
The gateway rejects a frame whose length is absurd (any printable byte read as a length is), answers
502 and replaces the worker.

## Environment variables

| Variable              | Read by                     | Default                         |                                                                    |
|-----------------------|-----------------------------|---------------------------------|--------------------------------------------------------------------|
| `PROJECT_ROOT`        | the entries of `compat/`    |                                 | the framework application's folder, when `-project-root` is not given (see [Existing frameworks](#existing-frameworks)) |
| `WARMPHP_PHP`       | `make test`                 | the Makefile's Docker command   | the PHP command of the tests; it must pass stdin through          |
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
