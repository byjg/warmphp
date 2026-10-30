# Benchmark: PHP-FPM against phpworker-gateway

The same user journey, run on the builds of
[gluo-framework-comparison](https://github.com/byjg/gluo-framework-comparison), served once by
nginx and PHP-FPM, which boots the application on every request, and once by nginx and
phpworker-gateway, which keeps it booted in its workers.

There are two suites:

- **fullstack**: each framework the way it builds a whole application (server-rendered pages, or
  Inertia for Laravel), against Gluo.
- **api**: each framework as a JSON API in front of Gluo's SPA, the same contract as Gluo's API.
  This one compares like with like: the same endpoints, the same JSON, a JWT everywhere.

And two journeys: **read** (log in, read the list) and **crud** (log in, page through 100
products, read one, edit it, add one, page through again), [below](#the-crud-journey).

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

## The read journey

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

Both sides run in one container of the same Docker image, `byjg/php:8.5-fpm-nginx` (PHP 8.5, its
`php`, `php-fpm` and `nginx`), with the same user, mounts and environment, **nginx in front** on
`127.0.0.1:18811`, and **4 PHP processes**:

- **fpm**: `php-fpm` with `pm = static` and 4 children, on a Unix socket; nginx serves the
  project's public folder and falls back to its front controller (`try_files $uri /index.php?...`),
  as the image does with `NGINX_ROOT` and `PHP_CONTROLLER`: `public/index.php` (Symfony,
  CodeIgniter, Laravel), `webroot/index.php` (CakePHP), Gluo's `public/app.php`.
- **worker**: the gateway (built static, `CGO_ENABLED=0`) on `127.0.0.1:18812`, with `-workers 4`
  and the boot file of `compat/`; its workers are the image's `php`, as its child processes (the
  frames go through plain pipes). nginx proxies to it over HTTP/1.1 with keep-alive.

The nginx configuration is the same on both sides except that `location`: the image's defaults
(worker processes, mime types), no access log, `client_max_body_size 1m` (the gateway's
`-max-body`). The image's start scripts are not used: they listen on port 80 as the image's user,
and here nginx and PHP run as the user that owns the projects (they write their caches), on the
host network (the databases are on `127.0.0.1`). So the benchmark writes the FPM pool and the nginx
configuration itself, with their temporary files in a folder of its own.

The **memory** column is what `docker stats` shows for that container after the run (without the
page cache): nginx, and PHP-FPM or the gateway with its workers.

4 clients run the journeys concurrently, after a warm-up of 5 % of the journeys.

OPcache on and off: `-d opcache.enable=0|1 -d opcache.enable_cli=0|1` on both sides (`php-fpm` reads
`opcache.enable`, the workers' CLI `opcache.enable_cli`).

Settings, given as environment variables (no file changed). Symfony's Dotenv only reads them from
`$_SERVER` and `$_ENV`: FPM keeps them (`clear_env = no`) and runs with `variables_order=EGPCS`;
the workers put the environment in `$_SERVER` themselves, as the CLI does. An API build runs with the settings of its full-stack sibling:

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

## Results: read journey

100 journeys per run, one build at a time. Intel Core Ultra 9 185H (22 CPUs), Docker, PHP 8.5.12.
MySQL 8.4 for the full-stack Laravel, CakePHP and CodeIgniter builds, 8.0 for the others. The
memory is with OPcache on.

### api: the same API, journeys per second (higher is better)

| Build               | OPcache on: FPM | worker |       | OPcache off: FPM | worker |       | memory: FPM | worker  |
|---------------------|----------------:|-------:|------:|-----------------:|-------:|------:|------------:|--------:|
| Gluo                |            16.4 |   16.2 | ×0.99 |             13.9 |   16.6 | ×1.19 |      48 MiB | 109 MiB |
| Symfony             |             8.8 |    9.6 | ×1.09 |              7.1 |    9.6 | ×1.35 |      59 MiB | 151 MiB |
| CakePHP             |            18.3 |   18.7 | ×1.02 |             13.2 |   18.6 | ×1.41 |      53 MiB | 134 MiB |
| CodeIgniter         |            17.1 |   16.3 | ×0.95 |             13.1 |   16.4 | ×1.25 |      52 MiB | 122 MiB |
| Laravel, new app    |            13.2 |   13.2 | ×1.00 |              8.0 |   13.3 | ×1.66 |      73 MiB | 241 MiB |
| Laravel, shared app |            13.2 |   16.1 | ×1.22 |              8.0 |   16.2 | ×2.03 |      73 MiB | 222 MiB |

**Average time of each step** (FPM → worker):

| Build               | OPcache | log in        | product list     |
|---------------------|---------|---------------|------------------|
| Gluo                | on      | 224 → 235 ms  | 16.2 → 8.7 ms    |
| Gluo                | off     | 248 → 230 ms  | 36.2 → 9.2 ms    |
| Symfony             | on      | 429 → 405 ms  | 17.2 → 5.2 ms    |
| Symfony             | off     | 478 → 405 ms  | 84.8 → 5.3 ms    |
| CakePHP             | on      | 207 → 205 ms  | 10.0 → 5.8 ms    |
| CakePHP             | off     | 248 → 205 ms  | 52.4 → 5.7 ms    |
| CodeIgniter         | on      | 214 → 226 ms  | 16.0 → 15.1 ms   |
| CodeIgniter         | off     | 251 → 224 ms  | 52.7 → 14.3 ms   |
| Laravel, new app    | on      | 263 → 259 ms  | 36.0 → 39.1 ms   |
| Laravel, new app    | off     | 360 → 259 ms  | 133.4 → 39.3 ms  |
| Laravel, shared app | on      | 263 → 236 ms  | 36.0 → 8.3 ms    |
| Laravel, shared app | off     | 360 → 234 ms  | 133.4 → 8.6 ms   |

### fullstack: each framework's own application, journeys per second

| Build               | OPcache on: FPM |         worker |       | OPcache off: FPM |         worker |       | memory: FPM | worker  |
|---------------------|----------------:|---------------:|------:|-----------------:|---------------:|------:|------------:|--------:|
| Symfony             |             7.8 |            8.6 | ×1.10 |              5.0 |            8.5 | ×1.70 |      64 MiB | 196 MiB |
| CakePHP             |            15.2 |           16.2 | ×1.07 |              9.0 |           15.9 | ×1.77 |      61 MiB | 162 MiB |
| CodeIgniter         |            10.5 |           10.5 | ×1.00 |              6.7 |           10.0 | ×1.49 |      61 MiB | 141 MiB |
| Laravel, new app    |             2.4 |            2.4 | ×1.00 |              2.1 |            2.3 | ×1.10 |     262 MiB | 441 MiB |
| Laravel, shared app |             2.4 | ❌ 84 % failed |     — |              2.1 | ❌ 84 % failed |     — |     262 MiB | 413 MiB |
| Gluo (an API)       |            16.4 |           16.2 | ×0.99 |             13.9 |           16.6 | ×1.19 |      48 MiB | 109 MiB |

**Average time of each step** (FPM → worker):

| Build            | OPcache | login page      | log in          | product list     |
|------------------|---------|-----------------|-----------------|------------------|
| Symfony          | on      | 12.6 → 4.7 ms   | 461 → 432 ms    | 32.0 → 19.8 ms   |
| Symfony          | off     | 77.8 → 5.1 ms   | 609 → 438 ms    | 103.8 → 21.0 ms  |
| CakePHP          | on      | 6.8 → 4.7 ms    | 232 → 223 ms    | 19.3 → 14.6 ms   |
| CakePHP          | off     | 43.2 → 5.0 ms   | 320 → 228 ms    | 71.7 → 16.0 ms   |
| CodeIgniter      | on      | 15.4 → 14.5 ms  | 297 → 304 ms    | 63.0 → 59.1 ms   |
| CodeIgniter      | off     | 59.7 → 15.7 ms  | 408 → 315 ms    | 118.7 → 63.2 ms  |
| Laravel, new app | on      | 311 → 304 ms    | 998 → 983 ms    | 365 → 363 ms     |
| Laravel, new app | off     | 374 → 317 ms    | 1116 → 984 ms   | 429 → 394 ms     |

The whole journey, p50 and p99, is in `benchmark/results.tsv` after a run.

## Reading the read results

- **The login is bcrypt, and it decides the journeys per second.** Cost 12 takes about 200 ms on
  this machine, whatever the framework or the server; Symfony's cost 13 takes 400 ms, which halves
  its rate. The worker cannot make it faster. A journey is one login and one list, so with OPcache
  on the two servers are within 10 % for every build but Laravel's shared app (Gluo and CodeIgniter
  are 1–5 % slower in the worker, within the noise of one run): compare the steps instead.
- **The product list is where the frameworks differ.** In the worker, as an API: Symfony 5.2 ms,
  CakePHP 5.8, Laravel (shared app) 8.3, Gluo 8.7, CodeIgniter 15.1, Laravel (new app) 39. With FPM
  and OPcache on: CakePHP 10.0 ms, CodeIgniter 16.0, Gluo 16.2, Symfony 17.2, Laravel 36.0.
  Without OPcache, FPM compiles every file on every request: Gluo 36 ms, CakePHP 52, CodeIgniter 53,
  Symfony 85, Laravel 133.
- **The worker removes the boot**: its times barely change with OPcache, while FPM boots on every
  request. The gain is largest without OPcache (×1.2 to ×2.0 for the API journey) and where the boot
  is heaviest: Symfony's list goes from 17 to 5 ms with OPcache, from 85 to 5 ms without.
- **The worker costs memory**: 1.7 to 3.3 times FPM's, 60 to 180 MiB more for 4 processes. Each worker
  keeps its booted application and its own OPcache (CLI processes do not share it); FPM's children
  share one OPcache and free the application after every request.
- **CodeIgniter gains nothing from the worker with OPcache on** (list 16.0 → 15.1 ms): most of its
  work happens on every request, booted or not.
- **The same framework is faster as an API** than as a full application: the list in the worker
  goes from 19.8 to 5.2 ms (Symfony), 14.6 to 5.8 (CakePHP), 59 to 15 (CodeIgniter, whose debug
  toolbar is injected into HTML pages only) and 363 to 39 ms (Laravel, new app). An API skips the
  session, the templates and, for Laravel, Inertia.
- **Laravel with a new application per request is no faster than FPM** with OPcache on, in both
  suites: it only keeps the autoloader warm, and `-max-requests 30` replaces each worker every 30
  requests (a cold boot).
- **Laravel with one shared application**: the full-stack build fails (84 of 100 logins answer 429:
  Fortify's login limiter keeps its counter in the shared application's memory, one of the leaks in
  [compatibility.md](compatibility.md)), but **the API build works** and is Laravel's best result
  (16.1 journeys/s, list 8.3 ms): it has no login limiter and no CSRF token. Passport's token is
  still checked in the database on every request.

## The crud journey

The read journey is one login and one list of 5 products, so it mostly measures bcrypt. The crud
journey keeps the login and adds reads and writes on a bigger table. Each framework does every
step the way it does it; no application is changed.

| # | Step     | fullstack                                                             | api (and Gluo)                     |
|---|----------|-----------------------------------------------------------------------|------------------------------------|
| 1 | page     | the login page                                                        | — (a static file of the SPA)       |
| 2 | login    | as in the read journey                                                | as in the read journey             |
| 3 | list1    | the product list, page 1                                              | `GET /product?page=0&size=50`      |
| 4 | list2    | the product list, page 2                                              | `GET /product?page=1&size=50`      |
| 5 | get      | the edit page of a random product (1–100)                             | `GET /product/{id}`                |
| 6 | edit     | the edit form posted with a new price, and the redirect it answers    | `PUT /product/{id}`, a new price   |
| 7 | add      | the new-product form, posted, and the redirect it answers             | `POST /product`                    |
| 8 | relist1  | page 1 again                                                          | page 1 again                       |
| 9 | relist2  | page 2 again                                                          | page 2 again                       |

The pages are each framework's own:

| Build                  | list pages                               | edit page               | after a save |
|------------------------|------------------------------------------|-------------------------|--------------|
| Symfony                | `/product` (no paging: the whole table)  | `/product/{id}/edit`    | the list     |
| CakePHP                | `/products?page=1&limit=50`, `page=2`    | `/products/edit/{id}`   | `/`          |
| CodeIgniter            | `/products?page=1`, `?page=2`            | `/products/edit/{id}`   | the list     |
| Laravel                | `/products` (no paging: the whole table) | `/products/{id}/edit`   | the list     |
| every API, Gluo        | `/product?page=0&size=50`, `page=1`      | —                       | —            |

So in the full-stack suite, *edit* and *add* include a list page (the redirect), and *get* is an
HTML form: compare their steps with each other, not with the API's.

**The data.** Before each run (each build, server and OPcache setting), the build is reset with its
own commands (tables recreated, seeds run), then one SQL statement adds products 6 to 100: 100
products, ids from 1. The run stops if the count or the first id is wrong. Each journey adds one
product, so a run ends with about 205.

| Build       | Reset                                                                                              |
|-------------|----------------------------------------------------------------------------------------------------|
| Symfony     | `doctrine:schema:drop --force --full-database`, `doctrine:migrations:migrate`, `doctrine:fixtures:load` |
| CakePHP     | `migrations rollback -t 0`, `migrations migrate`, `seeds run -q -f`                                |
| CodeIgniter | `spark migrate:refresh --all -f`, `spark db:seed ProductSeeder`                                    |
| Laravel     | `artisan migrate:fresh --seed --force`                                                             |
| Gluo        | `gluo --env=dev migrate:reset`                                                                     |

Symfony's fixtures are loaded into a recreated schema, not with `--purge-with-truncate`: MySQL
commits on `TRUNCATE`, which breaks Doctrine's transaction.

### Results: crud journey, journeys per second (higher is better)

100 journeys per run, one build at a time, same machine and settings as above. The memory is with
OPcache on.

**api**

| Build               | OPcache on: FPM | worker |       | OPcache off: FPM | worker |       | memory: FPM | worker  |
|---------------------|----------------:|-------:|------:|-----------------:|-------:|------:|------------:|--------:|
| Gluo                |            12.5 |   14.7 | ×1.18 |              8.1 |   14.4 | ×1.78 |      48 MiB | 110 MiB |
| Symfony             |             7.2 |    8.5 | ×1.18 |              3.6 |    8.6 | ×2.39 |      61 MiB | 165 MiB |
| CakePHP             |            12.9 |   15.5 | ×1.20 |              5.8 |   15.0 | ×2.59 |      55 MiB | 164 MiB |
| CodeIgniter         |            10.9 |   11.0 | ×1.01 |              5.8 |   11.6 | ×2.00 |      55 MiB | 128 MiB |
| Laravel, new app    |             7.2 |    6.3 | ×0.88 |              3.0 |    6.9 | ×2.30 |      74 MiB | 225 MiB |
| Laravel, shared app |             7.2 |   12.1 | ×1.68 |              3.0 |   12.2 | ×4.07 |      74 MiB | 225 MiB |

**fullstack**

| Build               | OPcache on: FPM |         worker |       | OPcache off: FPM |         worker |       | memory: FPM | worker  |
|---------------------|----------------:|---------------:|------:|-----------------:|---------------:|------:|------------:|--------:|
| Symfony             |             5.8 |            6.9 | ×1.19 |              2.3 |            6.8 | ×2.96 |      68 MiB | 213 MiB |
| CakePHP             |             8.8 |            9.9 | ×1.13 |              3.8 |            9.9 | ×2.61 |      60 MiB | 175 MiB |
| CodeIgniter         |             4.7 |            4.5 | ×0.96 |              2.4 |            4.9 | ×2.04 |      68 MiB | 147 MiB |
| Laravel, new app    |             1.0 |            1.0 | ×1.00 |              0.8 |            0.9 | ×1.13 |     241 MiB | 450 MiB |
| Laravel, shared app |             1.0 | ❌ 84 % failed |     — |              0.8 | ❌ 84 % failed |     — |     241 MiB | 406 MiB |
| Gluo (an API)       |            12.5 |           14.7 | ×1.18 |              8.1 |           14.4 | ×1.78 |      48 MiB | 110 MiB |

**Average time of each step**, worker and OPcache on. "7 steps" is the sum of the steps after the
login (list1 to relist2), the framework's own work; the last column is the same sum with FPM and
OPcache on.

| api                 | log in | list1   | list2   | get     | edit    | add     | 7 steps | 7 steps, FPM |
|---------------------|-------:|--------:|--------:|--------:|--------:|--------:|--------:|-------------:|
| Gluo                | 216 ms | 8.8 ms  | 8.4 ms  | 1.6 ms  | 10.2 ms | 9.0 ms  |   55 ms |        95 ms |
| Symfony             | 412 ms | 6.3 ms  | 5.6 ms  | 3.0 ms  | 12.6 ms | 10.3 ms |   48 ms |       125 ms |
| CakePHP             | 207 ms | 6.2 ms  | 5.6 ms  | 3.4 ms  | 12.9 ms | 10.9 ms |   50 ms |        88 ms |
| CodeIgniter         | 230 ms | 16.8 ms | 16.2 ms | 15.1 ms | 24.1 ms | 22.6 ms |  127 ms |       136 ms |
| Laravel, new app    | 283 ms | 54.9 ms | 47.2 ms | 42.7 ms | 50.2 ms | 45.6 ms |  326 ms |       278 ms |
| Laravel, shared app | 242 ms | 14.2 ms | 14.0 ms | 4.6 ms  | 14.2 ms | 11.7 ms |   86 ms |       278 ms |

| fullstack           | page    | log in | list1   | list2   | get     | edit    | add     | 7 steps  | 7 steps, FPM |
|---------------------|--------:|-------:|--------:|--------:|--------:|--------:|--------:|---------:|-------------:|
| Symfony             | 4.9 ms  | 424 ms | 17.0 ms | 16.7 ms | 8.7 ms  | 30.3 ms | 35.8 ms |   142 ms |       243 ms |
| CakePHP             | 4.8 ms  | 223 ms | 25.7 ms | 25.9 ms | 6.7 ms  | 26.0 ms | 29.3 ms |   165 ms |       210 ms |
| CodeIgniter         | 17.7 ms | 342 ms | 69.0 ms | 67.1 ms | 22.8 ms | 96.5 ms | 115 ms  |   516 ms |       511 ms |
| Laravel, new app    | 248 ms  | 868 ms | 366 ms  | 369 ms  | 280 ms  | 680 ms  | 659 ms  |  3047 ms |      2926 ms |

The whole journey, p50 and p99, and every setting are in `benchmark/results-crud.tsv` after a run.

### Who wins what

One run of 100 journeys per setting: a difference under about 10 % is a tie.

**Best overall (journeys per second)**: **CakePHP API**, 15.5 in the worker and 12.9 with FPM,
then Gluo (14.7 / 12.5), Laravel API with a shared app (12.1, worker only), CodeIgniter API
(11.0 / 10.9), CakePHP full-stack (9.9 / 8.8) and Symfony API (8.5 / 7.2). CakePHP is first mostly
because its login is the cheapest (207 ms against Gluo's 216): the steps after it barely separate
the two.

| Category                                    | Winner                                  | Numbers                                                                                     |
|---------------------------------------------|-----------------------------------------|---------------------------------------------------------------------------------------------|
| Fastest framework code, worker              | tie: **Symfony API, CakePHP API**, Gluo | 48 / 50 / 55 ms for the 7 steps                                                             |
| Fastest framework code, FPM + OPcache       | tie: **CakePHP API, Gluo**              | 88 / 95 ms; Symfony 125, CodeIgniter 136                                                    |
| Without OPcache, FPM                        | **Gluo**                                | 8.1 journeys/s against 5.8 for CakePHP and CodeIgniter; 246 ms for the 7 steps against 408–419 |
| Read one product                            | **Gluo**                                | 1.6 ms; Symfony 3.0, CakePHP 3.4                                                            |
| Writes (edit + add)                         | **Gluo**                                | 10.2 + 9.0 ms; Symfony 12.6 + 10.3, CakePHP 12.9 + 10.9                                     |
| Pages of 50 products                        | **Symfony API, CakePHP API**            | 5.6–6.3 ms; Gluo 8.4–8.8                                                                    |
| Least memory                                | **Gluo**, on both servers               | 48 MiB with FPM, 110 MiB in the worker; the other APIs 55–74 and 128–225                    |
| Largest gain from the worker                | **Laravel, shared app**                 | 278 → 86 ms for the 7 steps (×3.2); Symfony ×2.6, CakePHP ×1.8, Gluo ×1.7                    |
| Steadiest (p99 against p50, API, OPcache on) | **CakePHP API**                        | p99 = 1.11 × p50 with FPM, 1.17 in the worker; CodeIgniter 1.7–1.9, Laravel new app 2.0     |
| Best full-stack build                       | **CakePHP**                             | 9.9 journeys/s; Symfony's steps are faster (142 against 165 ms), but its login costs 424 ms |
| Slowest                                     | **Laravel full-stack**                  | 1.0 journey/s: every list sends the whole table through Inertia, about 360 ms each          |

### Reading the crud results

- **The login still decides the journeys per second.** It is 200–420 ms of bcrypt against 50–150 ms
  for the 7 other steps in the worker. Symfony hashes with cost 13 (`auto`), about 200 ms more per
  login; at cost 12 its API would be near 15 journeys/s, level with CakePHP (an estimate, not
  measured).
- **With OPcache on, the worker saves 40 to 80 ms per journey** on the APIs that keep their
  application booted (Gluo, Symfony, CakePHP; 190 ms for Laravel's shared app): ×1.2 the journeys
  per second (×1.7 for Laravel), ×1.7 to ×3.2 on the 7 steps. Without OPcache FPM compiles every
  file on every request, and the worker is ×1.8 to ×4 faster on the APIs, since its times do not
  change with OPcache.
- **Gluo, Symfony and CakePHP are level as APIs in the worker**, about 50 ms for 7 steps. Gluo is
  ahead on the single read and the writes, Symfony and CakePHP on the pages of 50. With FPM, Gluo
  and CakePHP are level (95 / 88 ms) and Symfony pays for its boot (125 ms).
- **The memory is the worker's price**: 1.9 to 3.1 times FPM's, 60 to 210 MiB more for 4 processes:
  about 15 MiB more per worker for Gluo, 25–30 for the Symfony and CakePHP APIs, 35–50 for Laravel. Each worker
  keeps its booted application and its own OPcache; FPM's children share one OPcache and free the
  application after every request.
- **CodeIgniter gains nothing from the worker with OPcache on** (136 → 127 ms as an API, 511 → 516 as
  a full application): most of its work happens on every request, booted or not. It also has the
  widest tails after Laravel's new app (p99 up to 2.5 × p50).
- **Laravel depends on how it runs in the worker.** With a new application per request it is slower
  than FPM (326 against 278 ms) and uses three times the memory; with one shared application it is
  the third API (86 ms). The full-stack shared application still fails on Fortify's login limiter
  (429), as in the read journey.
- **Full-stack costs follow the list.** The builds without paging pay for the whole table on each of
  the four list pages and on each redirect after a save: Laravel's Inertia JSON (about 360 ms) more
  than Symfony's Twig (17 ms). CodeIgniter pages but renders its debug toolbar in `development`
  (about 68 ms a page).

### Screenshots

Taken after each build's crud run: the login page and the product list, with
the prices the journeys edited and the `Bench …` products they added. Each list keeps its own
order: Symfony and CakePHP by id, CodeIgniter and Gluo by name, Laravel newest first. The API
builds have none: they all serve Gluo's SPA, unchanged, so their pages look like Gluo's.

The pages are what each framework gives. Symfony's are `make:crud`'s templates, unstyled (the
sky-blue background is the AssetMapper recipe's `app.css`); CodeIgniter has no view generator, so
its two views are written by hand in plain HTML; CakePHP's `bake` templates come with its CSS;
Laravel's are the React starter kit's. The screenshots use the benchmark's settings: Symfony in
`prod`, CodeIgniter in `development`, which shows its debug toolbar button (bottom right).

| Build              | Login                                                             | Products                                                                |
|--------------------|-------------------------------------------------------------------|-------------------------------------------------------------------------|
| Symfony            | [login](../benchmark/screenshots/fullstack-symfony-login.jpg)     | [products](../benchmark/screenshots/fullstack-symfony-products.jpg)     |
| CakePHP            | [login](../benchmark/screenshots/fullstack-cakephp-login.jpg)     | [products](../benchmark/screenshots/fullstack-cakephp-products.jpg)     |
| CodeIgniter        | [login](../benchmark/screenshots/fullstack-codeigniter-login.jpg) | [products](../benchmark/screenshots/fullstack-codeigniter-products.jpg) |
| Laravel            | [login](../benchmark/screenshots/fullstack-laravel-login.jpg)     | [products](../benchmark/screenshots/fullstack-laravel-products.jpg)     |
| Gluo               | [login](../benchmark/screenshots/gluo-login.jpg)                  | [products](../benchmark/screenshots/gluo-products.jpg)                  |

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
BENCH_JOURNEY=crud BENCH_ONLY=api-cakephp BENCH_JOURNEYS=100 go test -tags compat -run TestBenchmark -v ./...  # the crud journey, one build
```

| Variable          | Default                              |                                                                                   |
|-------------------|--------------------------------------|-----------------------------------------------------------------------------------|
| `BENCH_JOURNEYS`  | 3000                                 | journeys per run                                                                  |
| `BENCH_CLIENTS`   | 4                                    | concurrent clients, and PHP processes on each server                              |
| `BENCH_SUITE`     |                                      | `fullstack` or `api`; Gluo runs in both                                           |
| `BENCH_ONLY`      |                                      | only the targets whose name contains it (`cakephp`, `api-laravel`...)             |
| `BENCH_SERVERS`   | `fpm,worker,prefork,frankenphp`      | the servers to run, comma separated                                               |
| `BENCH_PHP_INI`   | `xdebug.mode=off`                    | PHP settings for every server, space separated (`name=value`); the image loads Xdebug |
| `BENCH_IMAGE`     | `byjg/php:8.5-fpm-nginx`             | the image of FPM and the gateway: php, php-fpm and nginx                          |
| `COMPAT_DIR`      | `../gluo-framework-comparison`       | the comparison repository (the compatibility tests read it too)                   |

The benchmark sets two more for FrankenPHP's worker script (`compat/frankenphp.php`):
`PHPWORKER_BOOT`, the boot file of `compat/` it runs, and `PHPWORKER_MAX_REQUESTS`, the build's
`-max-requests` (it ends the script after that many requests, and FrankenPHP starts it again).

100 journeys per run take about 25 minutes for the read journey and 45 for the crud journey, both
suites. `results.tsv` (read) and `results-crud.tsv` (crud) are rewritten unless `BENCH_ONLY` is set,
which appends. The results above were run one build at a time, with `BENCH_SUITE` and `BENCH_ONLY`. The crud journey resets the
databases before every run (see [The crud journey](#the-crud-journey)): it erases what they had. The full-stack Laravel
shared-application runs fail by design (see above), so `go test` reports FAIL for them. The run
writes to the projects' ignored folders and databases: sessions (Laravel's `sessions` table, CodeIgniter's
`writable/session`), Passport's tokens (`oauth_access_tokens`) and logs.
