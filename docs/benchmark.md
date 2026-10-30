# Benchmark: php -S against phpworker-gateway

The same user journey, run on the builds of
[gluo-framework-comparison](https://github.com/byjg/gluo-framework-comparison), served once by
PHP's built-in server (`php -S`), which boots the application on every request as PHP-FPM does,
and once by phpworker-gateway, which keeps it booted in its workers.

There are two suites:

- **fullstack**: each framework the way it builds a whole application (server-rendered pages, or
  Inertia for Laravel), against Gluo.
- **api**: each framework as a JSON API in front of Gluo's SPA, the same contract as Gluo's API.
  This one compares like with like: the same endpoints, the same JSON, a JWT everywhere.

## The projects

| Build                              | Framework                                       | How it runs in the worker                                                                                                                                       |
|------------------------------------|-------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `frameworks/fullstack/symfony-official` | Symfony 8.1, Doctrine, Twig, form login    | [`compat/symfony.php`](../compat/symfony.php): one kernel                                                                                                       |
| `frameworks/fullstack/cakephp`     | CakePHP 5.4, Authentication plugin              | [`compat/cakephp.php`](../compat/cakephp.php): one application, a new DI container per request                                                                  |
| `frameworks/fullstack/codeigniter` | CodeIgniter 4.7, Shield                         | [`compat/codeigniter.php`](../compat/codeigniter.php): its own worker mode                                                                                      |
| `frameworks/fullstack/laravel`     | Laravel 13, Inertia (React), Fortify            | [`compat/laravel-fresh.php`](../compat/laravel-fresh.php): a new application per request; [`compat/laravel-shared.php`](../compat/laravel-shared.php): one application |
| `frameworks/gluo`                  | Gluo 7.0, JSON API with JWT, React SPA          | [`compat/gluo.php`](../compat/gluo.php): one `ByJG\Gluo\Http\Application`                                                                                       |
| `frameworks/api/symfony`           | Symfony 8.1, Doctrine, lexik JWT                | `compat/symfony.php`                                                                                                                                            |
| `frameworks/api/cakephp`           | CakePHP 5.4, Authentication plugin (Jwt)        | `compat/cakephp.php`                                                                                                                                            |
| `frameworks/api/codeigniter`       | CodeIgniter 4.7, Shield JWT                     | `compat/codeigniter.php`                                                                                                                                        |
| `frameworks/api/laravel`           | Laravel 13, Passport (JWT access tokens)        | `compat/laravel-fresh.php` and `compat/laravel-shared.php`                                                                                                      |

The boot files are the same for both suites: they only need the project's root.
[docs/compatibility.md](compatibility.md) explains each one. No application is changed for the
benchmark.

## The journey

Every iteration is one visitor, with a new browser (cookies, connections): log in as the seeded
admin, then read the product list from the database. A journey only counts if the list really has
products; anything else is an error.

**fullstack** opens the login page first:

| Build       | 1. login page       | 2. log in (and the redirect it answers)            | 3. product list               |
|-------------|---------------------|----------------------------------------------------|-------------------------------|
| Symfony     | `GET /login`        | `POST /login`, form with CSRF                      | `GET /product`                |
| CakePHP     | `GET /users/login`  | `POST /users/login`, form with CSRF                | `GET /products`               |
| CodeIgniter | `GET /login`        | `POST /login`, form with CSRF                      | `GET /products`               |
| Laravel     | `GET /login`        | `POST /login`, JSON with XSRF, as Inertia sends it | `GET /products`, Inertia JSON |

**api** (and Gluo in both): the login page is a static file of the SPA, not PHP.

| Build      | 1. login page | 2. log in                          | 3. product list                   |
|------------|---------------|------------------------------------|-----------------------------------|
| every API  | —             | `POST /login`, JSON, returns a JWT | `GET /product`, JSON with the JWT |

The code is [`benchmark_test.go`](../benchmark_test.go): the journeys, the two servers and the
measures. It reuses the browser of the compatibility tests (`compat_test.go`).

## How each side runs

Both sides run PHP 8.5 in the same Docker image (`byjg/php:8.5-cli-load`), with the same user, mounts
and environment, and **4 PHP processes**:

- **php -S**: `php -S 127.0.0.1:18811 <the project's router>` with `PHP_CLI_SERVER_WORKERS=4`.
  The routers are the ones the projects document: `public/index.php` (Symfony), `webroot/index.php`
  (CakePHP), CodeIgniter's `system/rewrite.php`, Laravel's `server.php`, Gluo's `public/app.php`.
- **worker**: the gateway with `-workers 4` and the boot file of `compat/`.

4 clients run the journeys concurrently, after a warm-up of 5 % of the journeys.

OPcache on and off: `-d opcache.enable=0|1 -d opcache.enable_cli=0|1` on both sides. Both are needed:
`php -S` (SAPI `cli-server`) ignores `opcache.enable_cli` and keeps OPcache on unless
`opcache.enable=0`.

Settings, given as environment variables (no file changed). An API build runs with the settings of
its full-stack sibling:

| Build       | Environment                                                                                                                                                        |
|-------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Symfony     | `APP_ENV=prod APP_DEBUG=0`                                                                                                                                         |
| CakePHP     | `DEBUG=false APP_FULL_BASE_URL=http://localhost:8084` (API: `:9083`; CakePHP requires it in production)                                                            |
| CodeIgniter | none: `development`, as the projects are configured                                                                                                                |
| Laravel     | `CACHE_STORE=array`, and for the full-stack build `DB_HOST=127.0.0.1 DB_PORT=3307` (its `.env` is Sail's); the array cache removes Fortify's limit of 5 logins per minute, which the database cache keeps across requests |
| Gluo        | `APP_ENV=dev`                                                                                                                                                      |

Laravel with a new application per request runs with `-max-requests 30`: each application stays in
memory with its database connection (see [compatibility.md](compatibility.md)), and MySQL accepts
151 connections.

**Every build hashes passwords with bcrypt**: cost 12, except Symfony, whose `auto` hasher uses
cost 13 (twice the work). Gluo's users moved from unsalted SHA-1 to bcrypt in Gluo 7.0
(byjg/authuser), so its login is no longer cheaper than the others'.

## Results

100 journeys per run. Intel Core Ultra 9 185H (22 CPUs), Docker, PHP 8.5.12. MySQL 8.4 for the
full-stack Laravel, CakePHP and CodeIgniter builds, 8.0 for the others.

### api: the same API, journeys per second (higher is better)

| Build               | OPcache on: php -S |         worker |       | OPcache off: php -S | worker |       |
|---------------------|-------------------:|---------------:|------:|--------------------:|-------:|------:|
| Gluo                |               16.2 |           18.4 | ×1.14 |                13.4 |   18.4 | ×1.37 |
| Symfony             |                8.8 |            9.8 | ×1.11 |                 6.9 |    9.8 | ×1.42 |
| CakePHP             |               18.6 |           19.2 | ×1.03 |                13.3 |   19.2 | ×1.44 |
| CodeIgniter         |               17.1 |           17.9 | ×1.05 |                12.4 |   17.9 | ×1.44 |
| Laravel, new app    |               13.2 |           12.1 | ×0.92 |                 8.0 |   12.8 | ×1.60 |
| Laravel, shared app |               13.2 |           16.8 | ×1.27 |                 8.0 |   16.7 | ×2.09 |

**Average time of each step** (php -S → worker):

| Build               | OPcache | log in        | product list     |
|---------------------|---------|---------------|------------------|
| Gluo                | on      | 230 → 215 ms  | 13.5 → 2.7 ms    |
| Gluo                | off     | 258 → 213 ms  | 30.5 → 2.8 ms    |
| Symfony             | on      | 424 → 400 ms  | 20.2 → 4.2 ms    |
| Symfony             | off     | 492 → 402 ms  | 80.9 → 4.2 ms    |
| CakePHP             | on      | 205 → 200 ms  | 9.5 → 5.0 ms     |
| CakePHP             | off     | 248 → 201 ms  | 51.6 → 5.2 ms    |
| CodeIgniter         | on      | 216 → 209 ms  | 16.4 → 14.9 ms   |
| CodeIgniter         | off     | 261 → 208 ms  | 55.5 → 13.2 ms   |
| Laravel, new app    | on      | 268 → 283 ms  | 32.3 → 44.2 ms   |
| Laravel, new app    | off     | 370 → 266 ms  | 126.1 → 44.1 ms  |
| Laravel, shared app | on      | — → 232 ms    | — → 5.3 ms       |
| Laravel, shared app | off     | — → 232 ms    | — → 5.4 ms       |

### fullstack: each framework's own application, journeys per second

| Build               | OPcache on: php -S |         worker |       | OPcache off: php -S |         worker |       |
|---------------------|-------------------:|---------------:|------:|--------------------:|---------------:|------:|
| Symfony             |                8.4 |            9.7 | ×1.15 |                 5.3 |            9.5 | ×1.79 |
| CakePHP             |               16.1 |           17.9 | ×1.11 |                 9.6 |           17.7 | ×1.84 |
| CodeIgniter         |               10.5 |           12.6 | ×1.20 |                 7.1 |           11.5 | ×1.62 |
| Laravel, new app    |                5.3 |            4.6 | ×0.87 |                 3.4 |            4.7 | ×1.38 |
| Laravel, shared app |                5.3 | ❌ 84 % failed |     — |                 3.4 | ❌ 84 % failed |     — |
| Gluo (an API)       |               16.2 |           18.4 | ×1.14 |                13.4 |           18.4 | ×1.37 |

**Average time of each step** (php -S → worker):

| Build            | OPcache | login page      | log in        | product list     |
|------------------|---------|-----------------|---------------|------------------|
| Symfony          | on      | 12.5 → 4.6 ms   | 437 → 398 ms  | 19.3 → 5.8 ms    |
| Symfony          | off     | 79.8 → 4.6 ms   | 576 → 404 ms  | 85.9 → 5.9 ms    |
| CakePHP          | on      | 6.8 → 4.4 ms    | 224 → 210 ms  | 13.5 → 7.5 ms    |
| CakePHP          | off     | 40.2 → 4.4 ms   | 315 → 212 ms  | 62.1 → 7.5 ms    |
| CodeIgniter      | on      | 16.4 → 11.1 ms  | 298 → 268 ms  | 56.1 → 37.7 ms   |
| CodeIgniter      | off     | 55.8 → 13.6 ms  | 400 → 284 ms  | 104.1 → 45.4 ms  |
| Laravel, new app | on      | 81.6 → 107.5 ms | 586 → 635 ms  | 77.6 → 111.0 ms  |
| Laravel, new app | off     | 185.7 → 99.0 ms | 799 → 640 ms  | 181.2 → 102.0 ms |

The whole journey, p50 and p99, is in `benchmark/results.tsv` after a run.

## Reading the results

- **The login is bcrypt, and it decides the journeys per second.** Cost 12 takes about 200 ms on
  this machine, whatever the framework or the server; Symfony's cost 13 takes 400 ms, which halves
  its rate. The worker cannot make it faster. A journey is one login and one list, so the rates
  above mostly measure the password check: compare the steps instead.
- **The product list is where the frameworks differ.** In the worker, as an API: Gluo 2.7 ms,
  Symfony 4.2, CakePHP 5.0, Laravel (shared app) 5.3, CodeIgniter 14.9, Laravel (new app) 44. With
  `php -S` and OPcache on, CakePHP is the fastest (9.5 ms), then Gluo (13.5), CodeIgniter (16.4),
  Symfony (20.2) and Laravel (32.3). Without OPcache, `php -S` compiles every file on every request:
  Gluo 30.5 ms, CakePHP 51.6, CodeIgniter 55.5, Symfony 80.9, Laravel 126.
- **The worker removes the boot**: its list times barely change with OPcache, while `php -S`
  boots on every request. The gain is largest without OPcache (×1.4 to ×2.1 for the journey) and
  where the boot is heaviest.
- **The same framework is faster as an API** than as a full application: the list in the worker
  goes from 5.8 to 4.2 ms (Symfony), 7.5 to 5.0 (CakePHP), 37.7 to 14.9 (CodeIgniter, whose debug
  toolbar is injected into HTML pages only) and 111 to 44 ms (Laravel, new app). An API skips the
  session, the templates and, for Laravel, Inertia.
- **Laravel with a new application per request is slower than `php -S` with OPcache on**, in
  both suites: it only keeps the autoloader warm, and `-max-requests 30` replaces each worker every
  30 requests (a new container, a cold boot: its p99 is 1.2 to 1.7 s).
- **Laravel with one shared application**: the full-stack build fails (84 of 100 logins answer 429:
  Fortify's login limiter keeps its counter in the shared application's memory), but **the API
  build works** and is Laravel's best result (16.8 journeys/s, list 5.3 ms): it has no login limiter
  and no CSRF token. Passport's token is still checked in the database on every request.

## Reproducing

From fresh clones of both repositories, side by side (`COMPAT_DIR` points elsewhere). Each build
has its own MySQL in Docker, and the ports do not overlap.

**fullstack and Gluo** — migrated and seeded as [compatibility.md](compatibility.md#reproducing)
shows.

**api** — in `gluo-framework-comparison` (each guide in its `docs/api-*.md` has the details):

```bash
cd frameworks/api/laravel && composer install && cp .env.example .env && php artisan key:generate \
  && php artisan passport:keys && docker compose up -d --wait && php artisan migrate:fresh --seed && cd -
cd frameworks/api/symfony && composer install && php bin/console lexik:jwt:generate-keypair \
  && docker compose up -d --wait && php bin/console doctrine:migrations:migrate -n \
  && php bin/console doctrine:fixtures:load -n && cd -
cd frameworks/api/cakephp && composer install && docker compose up -d --wait \
  && bin/cake migrations migrate && bin/cake seeds run -q && cd -
cd frameworks/api/codeigniter && composer install && cp .env.example .env && docker compose up -d --wait \
  && php spark migrate --all && php spark db:seed ProductSeeder && cd -
```

Optionally, check each API against the contract of Gluo's SPA (it needs the API served, e.g.
`php artisan serve --port=9081`):

```bash
php frameworks/api/contract-test.php http://localhost:9081 http://localhost:7081
```

Then, from this repository:

```bash
BENCH_JOURNEYS=10 go test -tags compat -run TestBenchmark -v ./...                       # smoke run
BENCH_JOURNEYS=100 go test -tags compat -run TestBenchmark -v -timeout 90m ./...         # these results, both suites
BENCH_SUITE=api BENCH_JOURNEYS=100 go test -tags compat -run TestBenchmark -v ./...      # one suite (Gluo is in both)
```

| Variable          | Default                          |                                                                          |
|-------------------|----------------------------------|--------------------------------------------------------------------------|
| `BENCH_JOURNEYS`  | 3000                             | journeys per run                                                         |
| `BENCH_CLIENTS`   | 4                                | concurrent clients, and PHP processes on each side                       |
| `BENCH_SUITE`     |                                  | `fullstack` or `api`; Gluo runs in both                                  |
| `BENCH_ONLY`      |                                  | only the targets whose name contains it (`cakephp`, `api-laravel`...)    |
| `COMPAT_DIR`      | `../gluo-framework-comparison`   | the comparison repository                                                |
| `PHPWORKER_IMAGE` | `byjg/php:8.5-cli-load`          | the PHP image                                                            |

100 journeys per run take about 11 minutes for the 40 runs of both suites. `results.tsv` is
rewritten unless `BENCH_ONLY` is set, which appends. The full-stack Laravel shared-application
runs fail by design (see above), so `go test` reports FAIL for them. The run writes to the projects'
ignored folders and databases: sessions (Laravel's `sessions` table, CodeIgniter's
`writable/session`), Passport's tokens (`oauth_access_tokens`) and logs.
