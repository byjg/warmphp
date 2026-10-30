# phpworker-gateway

Keep a PHP application booted in memory and serve HTTP with it, using only the stock
PHP CLI binary. No custom SAPI, no PHP-FPM, no extension.

```text
Caddy / HAProxy / nginx / static-httpserver
              │  plain HTTP (reverse proxy)
              ▼
      phpworker-gateway (Go)
        - parses HTTP, builds $_SERVER (CGI/1.1 variables)
        - keeps N workers alive, one request per worker at a time
        - restarts crashed workers, kills slow ones, recycles after max-requests
              │  stdin/stdout, length-prefixed JSON frames
   ┌──────────┼──────────┐
 php worker.php   php worker.php   php worker.php   ← boot once, stay in memory
```

## Quick start

```bash
go build -o phpworker-gateway .
./phpworker-gateway -workers 4 examples/hello/app.php
curl 'localhost:8080/?name=joao'   # requests_served keeps growing: the app is not rebooted
```

Put any reverse proxy in front of it, e.g. Caddy: `reverse_proxy localhost:8080`.

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
./phpworker-gateway examples/restserver/app.php
curl 'localhost:8080/hello?name=joao'
```

## Existing frameworks

Symfony, CakePHP, Laravel, CodeIgniter and Gluo (7.0, through gluo-core's `Application`) were tested without changing them, through the boot files in
[`compat/`](compat). [docs/compatibility.md](docs/compatibility.md) lists what worked, what each
framework needs and the leaks found.

## Benchmark

[docs/benchmark.md](docs/benchmark.md) runs the same user journey (login page, login, product list) on
each framework, served by `php -S` and by the gateway, with OPcache on and off.

## Options

| Flag             | Default          | Description                                                          |
|------------------|------------------|----------------------------------------------------------------------|
| `-listen`        | `:8080`          | Address to listen on                                                 |
| `-php`           | `php`            | Command that runs the PHP CLI; may include arguments                 |
| `-worker`        | `php/worker.php` | Path to the worker script                                            |
| `-mode`          | `callable`       | `callable` or `script`                                               |
| `-workers`       | number of CPUs   | Number of PHP processes                                              |
| `-max-requests`  | `0`              | Replace a worker after this many requests (0 = never)                |
| `-timeout`       | `30s`            | Maximum time to wait for a worker and process a request (then 504 and the worker is killed) |
| `-boot-timeout`  | `30s`            | Maximum time for a worker to boot                                    |
| `-max-body`      | `32MiB`          | Maximum request body (413 above it)                                  |
| `-upload-dir`    | system temp dir  | Where uploaded files are written; PHP must see the same path         |

Running PHP in Docker works too. Use `--init`, so PHP is not PID 1 and stops on SIGTERM, and use `-i`
for stdin:

```bash
./phpworker-gateway \
  -php "docker run --rm -i --init -v $PWD:$PWD -v /tmp:/tmp -u $(id -u):$(id -g) byjg/php:8.5-cli php" \
  examples/hello/app.php
```

## Protocol

Workers start as `php -d disable_functions=<list> worker.php <mode> <entry>`; the gateway gets
`<list>` once from `php worker.php disable-functions`.

A frame is a 4-byte big-endian length followed by JSON. After booting, the worker sends the frame
`{"ready":true}`. Then, for each request, the gateway sends a frame on stdin, with a new random
boundary, and the worker answers on stdout with the raw body, the boundary, and a frame:

```text
gateway → worker   {"boundary":"--phpworker-<random>","server":{...CGI vars...},"body":"<base64>","fields":[[name,value]],"files":[{field,name,type,tmp_name,error,size}]}
worker  → gateway  <body bytes>--phpworker-<random>{"status":200,"headers":["Content-Type: text/html"],"exit":false}
```

Output outside a request, such as boot messages, goes to stderr, so it never mixes with a body.
The gateway rejects a frame whose length is absurd (any printable byte read as a length is), answers
502 and replaces the worker.

## Tests

The tests run real PHP workers. `PHPWORKER_PHP` sets the PHP command, which must pass stdin through:

```bash
PHPWORKER_PHP="docker run --rm -i --init -v $PWD:$PWD -v /tmp:/tmp -u $(id -u):$(id -g) byjg/php:8.5-cli php" \
  go test -race ./...
```
