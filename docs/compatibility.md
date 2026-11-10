---
sidebar_position: 2
sidebar_label: "Frameworks"
---

# Running existing frameworks

The builds of [gluo-framework-comparison](https://github.com/byjg/gluo-framework-comparison)
(commit `0f1c36f`) were run behind the gateway **without changing a file in them**. Everything
framework-specific lives in a boot file in [`compat/`](https://github.com/byjg/warmphp/tree/master/compat), the same role Laravel Octane,
RoadRunner or FrankenPHP adapters play.

| Build              | Version                         | Boot file                                                                                  |
|--------------------|---------------------------------|--------------------------------------------------------------------------------------------|
| `symfony-official` | Symfony 8.1.8                   | [`compat/symfony.php`](https://github.com/byjg/warmphp/blob/master/compat/symfony.php)                                              |
| `cakephp`          | CakePHP 5.4.3                   | [`compat/cakephp.php`](https://github.com/byjg/warmphp/blob/master/compat/cakephp.php)                                              |
| `codeigniter`      | CodeIgniter 4.7.5               | [`compat/codeigniter.php`](https://github.com/byjg/warmphp/blob/master/compat/codeigniter.php)                                      |
| `laravel`          | Laravel 13.35, Inertia 3.5      | [`compat/laravel-fresh.php`](https://github.com/byjg/warmphp/blob/master/compat/laravel-fresh.php), [`compat/laravel-shared.php`](https://github.com/byjg/warmphp/blob/master/compat/laravel-shared.php) |
| `gluo`             | Gluo 7.0, RestServer 7.0 (JSON API) | [`compat/gluo.php`](https://github.com/byjg/warmphp/blob/master/compat/gluo.php)                                                |
| the byjg/gluo starter | Gluo 7.0 (`ByJG\Gluo\Http\Application`) | [`compat/gluo.php`](https://github.com/byjg/warmphp/blob/master/compat/gluo.php)                                              |

Not tested: `symfony/` (the FrankenPHP template, same application as `symfony-official`).

Gluo runs in a worker since byjg/gluo-core 7.0: its `public/app.php` is
`Config::get(Application::class)->handle()`, and `compat/gluo.php` gets that `Application` once and
calls `handle()` with each request as a PSR-7 request. It needs, merged in the `7.0` branches:
byjg/restserver's `Server::withRequestObject()` and `Server::handle()` resetting its request and
response (see [points 2 and 5](#2-phpinput-is-empty)), and byjg/gluo-core's `Application`, with the
routes bound as `RouteListInterface::class`. Older Gluo projects are not supported.

The case `gluo` runs the comparison's `frameworks/gluo`. The case `gluo-starter` runs a project
created from the starter (`COMPAT_GLUO_STARTER`): its sample endpoints, public and behind a JWT, a
login, and the leak check.

CodeIgniter runs in `development`, as the project is configured, with its debug toolbar.

## Results

[`compat_test.go`](https://github.com/byjg/warmphp/blob/master/compat_test.go) drives each build over HTTP with one worker, so every request
goes through the same PHP process: anonymous access redirected to login, wrong password, login,
list, **an anonymous client right after an admin request** (the leak check), create, edit,
soft delete, logout. Gluo's scenario is the same over its JSON API, with a JWT.

| Build                          | Front controller as is (`-mode legacy`)                          | Boot file (`-mode worker`) | Leak check      | Memory                       |
|--------------------------------|------------------------------------------------------------------|------------------------------|-----------------|------------------------------|
| Symfony                        | ⚠️ works, but its runtime calls `exit`: a new worker per request  | ✅ full scenario              | ✅ no leak       | prod +1 KB/request, dev +3 KB/request |
| CakePHP                        | ✅ pages render (not tested further)                              | ✅ full scenario              | ✅ no leak       | flat                         |
| Laravel, new app per request   | ❌ 500 from the 2nd request                                       | ✅ full scenario              | ✅ no leak       | ❌ +0.5 MB and 1 DB connection per request |
| Laravel, one shared app        | —                                                                | ✅ full scenario              | ✅ no leak       | flat                         |
| CodeIgniter                    | ❌ routed as a CLI command                                        | ✅ full scenario              | ✅ no leak       | flat                         |
| Gluo                           | ❌ `class App` declared twice; uncaught 404s become 500           | ✅ full scenario              | ✅ no leak       | flat                         |

Average time of the login page (Gluo: the product list, authenticated) over 100 sequential requests after 10 warm-up requests, OPcache on
(`-d opcache.enable_cli=1`), PHP 8.5 in Docker. "Boots per request" is `php -S` with the
project's own router, which boots the application on every request like PHP-FPM:

| Build                                    | Boots per request | Worker  |
|------------------------------------------|------------------:|--------:|
| Symfony, `APP_ENV=prod`                  | 10.3 ms           | 3.8 ms  |
| Symfony, `dev` (profiler on)             | 78.9 ms           | 49.4 ms |
| CakePHP, `DEBUG=false`                   | 5.4 ms            | 3.9 ms  |
| Laravel, new app per request             | 42.8 ms           | 40.3 ms |
| Laravel, one shared app                  | 42.8 ms           | 16.9 ms |
| CodeIgniter (development, debug toolbar) | 14.5 ms           | 9.8 ms  |
| Gluo, `APP_ENV=dev`                      | 7.1 ms            | 2.5 ms  |

The worker itself stays at 0.46 MB over 2000 requests (`examples/hello`), so the memory growth
above is the frameworks'.

## Incompatibilities every framework hits

### 1. `PHP_SAPI` is `"cli"`

Symfony, Laravel, CodeIgniter and CakePHP check it, and take their console path: CLI requests,
console error renderers, no real session. Gluo and RestServer do not check it. The constant cannot change, but each framework has a switch, used by the boot files:

| Framework   | Effect when it sees `cli`                                                                 | Switch                                                                                     |
|-------------|-------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------|
| Symfony     | error pages use the CLI renderer (a variable dump instead of HTML)                        | `APP_RUNTIME_MODE=web=1&worker=1`, what `symfony/runtime` sets for FrankenPHP              |
| Laravel     | `$app->runningInConsole()` is `true`                                                      | `APP_RUNNING_IN_CONSOLE=false`, what Octane sets                                           |
| CodeIgniter | every request is routed as a CLI command (`Can't find a route for 'CLI: /'`), and `Boot` fixes the app context to `php-cli` | define `is_cli()` before `system/Common.php` loads (it is declared inside `if (! function_exists('is_cli'))`) |
| Kint (CodeIgniter's debug toolbar) | dumps render as text, which the toolbar cannot `json_encode()`: 500 on pages with view data | `Kint::$cli_detection = false` |
| CakePHP     | `Session` never starts a PHP session (`_isCLI`), errors render as console text, `bootstrap_cli.php` and CLI-only plugins load, logs go to `logs/cli-*.log` | no switch for the session: the boot file passes a `Session` subclass with `_isCLI = false`; the error renderers are set with `Configure::write('Error.*Renderer')` |

### 2. `php://input` is empty

Each framework has an official way to give the request its body:

| Framework           | How the boot file passes `PhpWorker::input()`                                          |
|---------------------|----------------------------------------------------------------------------------------|
| Symfony and Laravel | `Request::setFactory()`, so `Request::createFromGlobals()` and `Request::capture()` keep working ([`compat/request-body.php`](https://github.com/byjg/warmphp/blob/master/compat/request-body.php)) |
| CakePHP             | `ServerRequestFactory::fromGlobals(['CAKEPHP_INPUT' => …])`                             |
| CodeIgniter         | a `pre_system` listener: `service('request')->setBody(PhpWorker::input())`. `run()` creates the request from `php://input` and fires `pre_system` right after, before any filter or controller reads it. The full-stack build only posts forms (`$_POST`); the API build reads JSON with `getJSON()`, which needs it |
| Gluo (RestServer)   | none: `HttpRequest::payload()` reads `php://input` itself, and Gluo's `Server` comes from its DI config. RestServer now has `Server::withRequestObject()`, which handles a PSR-7 request (byjg/webrequest's `ServerRequest`, with the worker's globals and body, built by [`compat/psr7-request.php`](https://github.com/byjg/warmphp/blob/master/compat/psr7-request.php)) through the new `Psr7RequestAdapter::toHttpRequest()`, and gluo-core's `Application::handle()` passes it on |

Laravel's Inertia screens post JSON, so the Laravel test proves this path. The API builds of
`frameworks/api/` all post JSON, so the benchmark's api suite proves it for every framework.

### 3. Front controllers cannot be included twice

`-mode legacy` runs `public/index.php` again on every request, which these files do not expect:

| Build       | What happens                                                                                          |
|-------------|-------------------------------------------------------------------------------------------------------|
| Symfony     | works only because its runtime calls `exit` after each response: the worker is replaced every time (a second include would do nothing, `require_once 'vendor/autoload_runtime.php'`) |
| Laravel     | `define('LARAVEL_START')` again; the error handler left by the previous request turns the warning into a 500. `require_once 'bootstrap/app.php'` would then return `true` |
| CodeIgniter | `exit(Boot::bootWeb($paths))` ends the worker on every request, besides the `is_cli()` problem         |
| CakePHP     | works                                                                                                 |

This is why each framework gets a boot file.

### 4. Closing every output buffer

Three of the frameworks close output buffers down to level 0 somewhere: CodeIgniter on every
request (the `pre_system` listener of the appstarter's `app/Config/Events.php`, `while
(ob_get_level() > 0) { ob_end_flush(); }`), Symfony's runtime (`Response::closeOutputBuffers(0, true)`)
and CakePHP's `WebExceptionRenderer::clearOutput()` outside the CLI.

The worker's first protocol carried the body inside a frame, captured from an output buffer, so the
closed buffer sent the page onto the protocol stream (502). The protocol now treats the worker's
stdout as the body, as a web SAPI does, and the buffer that captures the body writes each chunk
there from its callback, which PHP still calls when the application closes it. The body must not go
through PHP's own output layer: in the CLI the first byte that does marks the headers as sent for
the rest of the process, and `session_start()`, `session_id()` and every `session.*` setting refuse
to work from then on (the first attempt at this protocol broke sessions that way). Output that
reaches it anyway, every buffer closed and then `echo`, is delivered in order and the worker is
replaced after that response; the real `headers_sent()` tells.

### 5. State that lives across requests

Each framework that supports workers ships a reset, and the boot files call it:

| Framework   | Reset                                                                                                   |
|-------------|---------------------------------------------------------------------------------------------------------|
| Symfony     | built in: `Kernel::handle()` runs the `services_resetter` before every request after the first           |
| CodeIgniter | `resetForWorkerMode()`, `Services::resetForWorkerMode()`, `Factories::reset()`, `Events::cleanupForWorkerMode()`, as in its FrankenPHP worker |
| CakePHP     | none. The DI container must be rebuilt per request (below)                                              |
| Laravel     | none without Octane                                                                                     |

Leaks found:

- **Gluo, singleton Server.** Gluo's config binds `Server` with `toSingleton()`, and the Server
  kept the response object of the request it handled: the next request answered with the
  previous one's status and body (the login answered the token of an earlier login with a 401).
  Fixed in RestServer: `handle()` starts each request with a new response and forgets the
  request after it, so one Server can serve many requests.
- **Gluo, uncaught exceptions.** RestServer throws its 404, 401 and 422 errors and relies on the
  exception handler it registers. In a worker they reach the worker instead (500). gluo-core's
  `Application::handle()` passes them to `ErrorHandler::handleException()` for a PSR-7 request,
  as the registered handler would.
- **CakePHP, DI container.** `BaseApplication::handle()` adds the request with
  `$container->add(ServerRequest::class, $request)`. League Container appends definitions and `get()`
  returns the first, so a persistent container gives every controller **the first request the
  worker ever served** (login failed: the controller saw an old request). It also grows by 4
  definitions per request. The boot file clears the container and the controller factory before
  each request.
- **CakePHP, flash messages.** `ServerRequest` builds its `FlashMessage` around the session it was
  created with, so replacing the session (point 1) also needs `withAttribute('flash', …)`.
- **CakePHP, DebugKit.** Its toolbar loads its panels again on every request (`The Cache alias has
  already been loaded`). CakePHP runs with `DEBUG=false`, which also needs `APP_FULL_BASE_URL`
  (CakePHP's host-header protection in production).
- **CakePHP, bootstrap.** `Server::run()` calls `bootstrap()` every time (`Plugin named DebugKit is
  already loaded`). The boot file runs the middleware itself and bootstraps once.
- **Laravel, one shared app.** Laravel's singletons assume one request per process. Kept across
  requests, three things break, and [`compat/laravel-shared.php`](https://github.com/byjg/warmphp/blob/master/compat/laravel-shared.php)
  resets them before every request, as Octane's listeners do:

  | State kept                                                                                             | What it caused                                                                                                  | Reset (Octane's listener)                                  |
  |--------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|------------------------------------------------------------|
  | `Session\Store::$attributes`: `loadSession()` merges what it reads into them                           | A client without cookies gets the previous client's `_token` and login: it sees the product list as the admin   | `FlushSessionState`: `flush()` and `regenerate()`          |
  | `DatabaseSessionHandler::$exists`, true after the first request                                        | New sessions are updated, not inserted, so never stored. With several workers the token comes from each worker's memory, so a POST on another worker answers 419 | `regenerate()` sets it back to false                       |
  | The `auth` guards and the `array` cache                                                                | The guard keeps its user; Fortify's limit of 5 logins per minute counts per worker and answers 429 (`CACHE_STORE=array`) | `FlushAuthenticationState`, `FlushArrayCache`              |

  With one worker the 419 never shows, since there is only one token: the benchmark, with 4 workers
  and 4 clients, is what found it. The API build was never affected: it has a JWT, no session.

  This is a part of what Octane does. It also clones the application per request and isolates the
  configuration, URLs, locale, Inertia and more: state outside the session, the login and the array
  cache is still shared. An application that changes it during a request (for example `config()`
  at runtime, or `App::setLocale()`) needs its own reset, or Octane. On FrankenPHP, Laravel's
  official path is Octane too (`bin/frankenphp-worker.php` runs `Laravel\Octane\Worker`).
- **Laravel, new app per request.** Every `Application` stays referenced (the error handlers and the
  `register_shutdown_function()` it registers at each bootstrap), with its database connection:
  +0.5 MB per request, and `SQLSTATE[HY000] [1040] Too many connections` at request 151 (MySQL's
  default `max_connections`). It works only with `-max-requests` below both limits (for example 100),
  and it is barely faster than booting per request: use the shared application instead.

## Changes made to the worker

These worker behaviours were wrong and are fixed, with tests in `gateway_test.go`:

- **The body is the worker's stdout** (point 4), with a random boundary per request ending it. It
  also removes the base64 copy of the body from the frame and streams the body in 64 KB chunks.
  `headers_sent()` is PHP's own again: it is accurate now, and the worker uses it.

- **Environment variables in `$_SERVER`.** The worker replaced `$_SERVER` on every request, so
  `DB_HOST` given to the container disappeared from it. The CLI, FrankenPHP and PHP-FPM with
  `clear_env = no` expose the environment there, and Laravel's dotenv loader skips `.env` values that
  already exist in `$_SERVER`. Request variables come first, the environment after.
- **Position of the session cookie.** The worker added the session cookie after the response.
  PHP sends it when `session_start()` or `session_regenerate_id()` runs, and frameworks rely on that
  order. Symfony sets or deletes the cookie itself afterwards. CakePHP's `Session::renew()` deletes
  the old cookie and then regenerates the id; with the worker's order, the browser deleted the new
  session and was logged out at once. The worker now inserts the cookie before the next header the
  application sets, which is where PHP would have sent it.

## What is left

- **Laravel** runs in a worker with one application and the resets of `compat/laravel-shared.php`, a part of Octane's; with a new app per request it needs `-max-requests`.
- An earlier memory test with Laravel, a new app per request, had two requests answered 502
  (`EOF`), right when memory ran out: the worker died before answering. It did not happen again,
  with either protocol, and the worker's own memory test (`/memory-full`) gets its 500.

## Reproducing

The databases are each project's own Docker Compose service, seeded with its documented commands:

```bash
cd ../gluo-framework-comparison
(cd frameworks/fullstack/symfony-official && docker compose up -d --wait database && php bin/console doctrine:migrations:migrate -n && php bin/console doctrine:fixtures:load -n)
(cd frameworks/fullstack/cakephp && docker compose up -d --wait database && bin/cake migrations migrate && bin/cake seeds run)
(cd frameworks/fullstack/codeigniter && docker compose up -d --wait database && php spark migrate --all && php spark db:seed ProductSeeder)
(cd frameworks/fullstack/laravel && docker compose up -d --wait mysql && DB_HOST=127.0.0.1 DB_PORT=3307 php artisan migrate:fresh --seed --force)
(cd frameworks/gluo && docker compose up -d --wait gluo-database && php gluo --env=dev migrate:reset)
(cd ../php-gluo && php gluo test)    # the starter's test database, on the same server
```

Both Gluo projects reach MySQL on `127.0.0.1:3306` from the host (`config/*/credentials.env`).
The comparison's `gluo-database` serves the starter's `localtest` database too: the starter's own
`docker-compose.database.yml` binds the same port, so only one of them can run.

`bin/cake migrations migrate` rewrites the tracked `cakephp/config/Migrations/schema-dump-default.lock`;
restore it with `git checkout` afterwards. Then:

```bash
COMPAT_GLUO_STARTER=$(cd ../php-gluo && pwd) go test -tags compat -run TestCompat -v ./...    # COMPAT_ONLY=cakephp runs one build
```

Fortify allows 5 login attempts per minute. Running the Laravel tests again within a minute gets a
429; `php artisan cache:clear` resets it. The runs write only the projects' ignored folders:
`var/`, `logs/`, `tmp/`, `storage/`, `writable/`.
