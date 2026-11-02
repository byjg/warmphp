# Benchmark: PHP-FPM, phpworker-gateway and FrankenPHP

The same user journey, run on the builds of
[gluo-framework-comparison](https://github.com/byjg/gluo-framework-comparison), by four servers:

- **fpm**: nginx and PHP-FPM, which boots the application on every request.
- **worker**: nginx and phpworker-gateway, one booted PHP process per worker.
- **prefork**: the same with `-prefork`: one boot, forked into the workers.
- **frankenphp**: FrankenPHP's worker mode, without nginx.

There are two suites:

- **fullstack**: each framework the way it builds a whole application (server-rendered pages, or
  Inertia for Laravel), against Gluo.
- **api**: each framework as a JSON API in front of Gluo's SPA, the same contract as Gluo's API.
  This one compares like with like: the same endpoints, the same JSON, a JWT everywhere.

The conclusions, per server and per framework, are in [analysis.md](analysis.md).

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

The boot files are the same for both suites, and for FrankenPHP: they only need the project's root.
[docs/compatibility.md](compatibility.md) explains each one. No application is changed for the
benchmark.

## The journey

Every iteration is one visitor, with a new browser (cookies, connections): log in as the seeded
admin, then a whole CRUD session. A journey only counts if every step did what it should (the list
has its products, the new price is there, the new product appears); anything else is an error.
Each framework does every step the way it does it; no application is changed.

| # | Step     | fullstack                                                             | api (and Gluo)                     |
|---|----------|-----------------------------------------------------------------------|------------------------------------|
| 1 | page     | the login page                                                        | — (a static file of the SPA)       |
| 2 | login    | the login form, with its CSRF token, and the redirect it answers      | `POST /login`, returns a JWT       |
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
| CodeIgniter | `spark migrate:refresh --all -f`, `spark db:seed ProductSeeder`, and the files of `writable/debugbar` and `writable/session` |
| Laravel     | `artisan migrate:fresh --seed --force`, and `storage/inertia-devtools`                             |
| Gluo        | `gluo --env=dev migrate:reset`                                                                     |

Symfony's fixtures are loaded into a recreated schema, not with `--purge-with-truncate`: MySQL
commits on `TRUNCATE`, which breaks Doctrine's transaction.

Two development tools write a file per request, and they stay on, as the projects are configured;
only their folders are cleared. Inertia's DevTools matters most: on every request it rewrites an
index of all its entries, under a lock. With 11,000 entries that is 3.9 MB per request, and full-stack
Laravel ran three times slower, with results that changed by up to 221 % between rounds.

## How each server runs

**fpm**, **worker** and **prefork** run in one container of the same Docker image,
`byjg/php:8.5-fpm-nginx` (Alpine, PHP 8.5.12 NTS, its `php`, `php-fpm` and `nginx`), with the same
user, mounts and environment, **nginx in front** on `127.0.0.1:18811`, and **4 PHP processes**:

- **fpm**: `php-fpm` with `pm = static` and 4 children, on a Unix socket; nginx serves the
  project's public folder and falls back to its front controller (`try_files $uri /index.php?...`),
  as the image does with `NGINX_ROOT` and `PHP_CONTROLLER`: `public/index.php` (Symfony,
  CodeIgniter, Laravel), `webroot/index.php` (CakePHP), Gluo's `public/app.php`.
- **worker**: the gateway (built static, `CGO_ENABLED=0`) on `127.0.0.1:18812`, with `-workers 4`
  and the boot file of `compat/`; its workers are the image's `php`, as its child processes (the
  frames go through plain pipes). nginx proxies to it over HTTP/1.1 with keep-alive.
- **prefork**: the same with `-prefork`; the workers are forks of one booted master, and talk to
  the gateway over a Unix socket.

The nginx configuration is the same for the three except that `location`: the image's defaults
(worker processes, mime types), no access log, `client_max_body_size 1m` (the gateway's
`-max-body`). The image's start scripts are not used: they listen on port 80 as the image's user,
and here nginx and PHP run as the user that owns the projects (they write their caches), on the
host network (the databases are on `127.0.0.1`). So the benchmark writes the FPM pool and the nginx
configuration itself, with their temporary files in a folder of its own.

**frankenphp** runs in its own image, [`benchmark/frankenphp/Dockerfile`](../benchmark/frankenphp/Dockerfile):
`dunglas/frankenphp:1-php8.5` (Debian, PHP 8.5.11 ZTS) with the extensions the projects need, and a
Caddyfile of its own: 4 workers, no admin API, no HTTPS, no compression. Caddy also serves the static
files, so nginx is not there. Its worker script is [`compat/frankenphp.php`](../compat/frankenphp.php),
which runs the same boot files of `compat/`.

**The two PHP builds are not equally fast**, which matters for the comparison. On this machine, in
the same CPU test, one process:

| PHP build                 | bcrypt, cost 12 | integer loop | md5 |
|---------------------------|----------------:|-------------:|----:|
| byjg (Alpine, NTS)        |        170.5 ms |       194 ms | 394 ms |
| FrankenPHP (Debian, ZTS)  |        161.0 ms |       209 ms | 379 ms |

So FrankenPHP's login is about 5 % faster whatever the server does.

The **memory** column is what `docker stats` shows for that container after the run (without the
page cache): nginx, and PHP-FPM or the gateway with its workers; or FrankenPHP alone. nginx with
this configuration and nothing behind it takes 18.6 MiB on this machine, because it starts one
process per CPU (22); with 4 processes, 6.3 MiB.

4 clients run the journeys concurrently, after a warm-up of 5 % of the journeys.

OPcache on and off: `-d opcache.enable=0|1 -d opcache.enable_cli=0|1`. Xdebug is off
(`-d xdebug.mode=off`): the byjg image loads it, in `develop` mode.

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

## Results

100 journeys per run, one build at a time, with only that build's database up: 84 runs, 3 rounds
each, 25,200 journeys, **no failed journey**. Each row is the round with the median journeys/s; the
`spread` column of [`benchmark/results.tsv`](../benchmark/results.tsv) is the difference between the rounds:
at most 8 %, except FPM with full-stack CakePHP (14 %: 11.3 to 13.0 journeys/s), so read a
difference under about 5 % as a tie. Each round is also in `benchmark/results-round<n>.tsv`.
Intel Core Ultra 9 185H (22 CPUs), 30 GB, Docker. MySQL 8.4 for the full-stack Laravel, CakePHP and
CodeIgniter builds, 8.0 for the others. A journey's login is bcrypt, about half of it or more, so
the "7 steps" tables below show the frameworks and the servers better.

The full-stack Laravel rows were run again after the reset of `storage/inertia-devtools` was added;
nothing else depends on it. The machine slept during round 2's Symfony runs; Symfony's spread is
2–7 %.

### Journeys per second (higher is better)

**api**

| Build               | OPcache on: fpm | worker | prefork | frankenphp | OPcache off: fpm | worker | prefork | frankenphp |
|---------------------|----------------:|-------:|--------:|-----------:|-----------------:|-------:|--------:|-----------:|
| Gluo                |            15.7 |   17.0 |    17.1 |       18.0 |              9.6 |   17.0 |    17.0 |       17.8 |
| Symfony             |             8.5 |    9.3 |     9.4 |        9.9 |              4.2 |    9.2 |     9.2 |        9.8 |
| CakePHP             |            16.2 |   16.9 |    17.5 |       18.5 |              7.0 |   17.3 |    17.4 |       18.2 |
| CodeIgniter         |            14.9 |   15.3 |    15.2 |       15.7 |              7.4 |   15.0 |    15.2 |       15.4 |
| Laravel, new app    |            12.6 |    9.7 |    12.0 |       13.3 |              3.9 |   10.3 |    10.3 |       11.4 |
| Laravel, shared app |            12.6 |   14.7 |    14.9 |       15.7 |              3.9 |   14.8 |    14.7 |       15.6 |

**fullstack** (Gluo is the API above)

| Build               | OPcache on: fpm | worker | prefork | frankenphp | OPcache off: fpm | worker | prefork | frankenphp |
|---------------------|----------------:|-------:|--------:|-----------:|-----------------:|-------:|--------:|-----------:|
| Symfony             |             7.3 |    8.8 |     8.7 |        9.0 |              2.8 |    8.3 |     8.4 |        8.9 |
| CakePHP             |            12.1 |   14.2 |    14.0 |       15.1 |              4.6 |   13.6 |    13.7 |       14.6 |
| CodeIgniter         |             9.4 |   10.3 |    10.5 |       10.4 |              4.2 |    9.7 |     9.7 |        9.8 |
| Laravel, new app    |             5.4 |    4.5 |     5.1 |        5.4 |              2.1 |    4.5 |     4.5 |        4.8 |
| Laravel, shared app |             5.4 |    5.7 |     5.8 |        5.9 |              2.1 |    5.7 |     5.6 |        5.8 |

### The 7 steps after the login, ms (lower is better)

The sum of the average time of list1 to relist2: the framework's own work, without bcrypt.

| Build                 | OPcache on: fpm | worker | prefork | frankenphp | OPcache off: fpm | worker | prefork | frankenphp |
|-----------------------|----------------:|-------:|--------:|-----------:|-----------------:|-------:|--------:|-----------:|
| Gluo                  |              48 |     28 |      26 |         26 |              184 |     26 |      29 |         26 |
| Symfony API           |              71 |     37 |      36 |         31 |              494 |     36 |      37 |         33 |
| CakePHP API           |              54 |     37 |      36 |         31 |              331 |     36 |      37 |         32 |
| CodeIgniter API       |              74 |     64 |      67 |         64 |              304 |     65 |      64 |         69 |
| Laravel API, new app  |              93 |    160 |     106 |         85 |              707 |    138 |     148 |        124 |
| Laravel API, shared   |              93 |     49 |      48 |         43 |              707 |     50 |      50 |         46 |
| Symfony full-stack    |             129 |     69 |      68 |         60 |              829 |     71 |      70 |         63 |
| CakePHP full-stack    |             126 |     82 |      82 |         72 |              531 |     84 |      84 |         73 |
| CodeIgniter full-stack |            183 |    151 |     144 |        152 |              582 |    165 |     163 |        164 |
| Laravel full-stack, new app |       288 |    389 |     306 |        285 |             1140 |    384 |     375 |        346 |
| Laravel full-stack, shared |        288 |    244 |     236 |        235 |             1140 |    250 |     252 |        245 |

### Memory, MiB (lower is better)

The whole container, with 4 PHP processes. FPM, worker and prefork include nginx (18.6 MiB here);
FrankenPHP is alone.

| Build                 | OPcache on: fpm | worker | prefork | frankenphp | OPcache off: fpm | worker | prefork | frankenphp |
|-----------------------|----------------:|-------:|--------:|-----------:|-----------------:|-------:|--------:|-----------:|
| Gluo                  |              47 |    109 |      59 |         49 |               59 |     93 |      63 |         58 |
| Symfony API           |              61 |    162 |      84 |         68 |               96 |    142 |     112 |         99 |
| CakePHP API           |              53 |    159 |      96 |         63 |               75 |    141 |     110 |         77 |
| CodeIgniter API       |              55 |    127 |      70 |         60 |               75 |    108 |      78 |         74 |
| Laravel API, new app  |              73 |    230 |     100 |        110 |              143 |    143 |     178 |        178 |
| Laravel API, shared   |              73 |    221 |     106 |        101 |              143 |    181 |     115 |        147 |
| Symfony full-stack    |              67 |    208 |     116 |         82 |              105 |    184 |     150 |        125 |
| CakePHP full-stack    |              61 |    172 |     106 |         69 |               81 |    151 |     119 |         88 |
| CodeIgniter full-stack |             68 |    149 |      89 |         77 |               90 |    134 |     104 |        100 |
| Laravel full-stack, new app |        99 |    304 |     180 |        201 |              169 |    269 |     178 |        289 |
| Laravel full-stack, shared |         99 |    258 |     137 |        134 |              169 |    218 |     148 |        184 |

Laravel with a new application per request is measured at whatever point `-max-requests 30` left it,
so its memory moves between rounds (178 to 240 MiB, full-stack prefork with OPcache off).

### Each step, OPcache on

Prefork and FrankenPHP, in ms. "page" is the login page, `list1` and `list2` are 50 products.

| Build                  | Server     | page | log in | list1 | list2 | get  | edit | add  | 7 steps |
|------------------------|------------|-----:|-------:|------:|------:|-----:|-----:|-----:|--------:|
| Gluo                   | prefork    |    — |    206 |   2.7 |   2.4 |  1.0 |  7.9 |  8.0 |      26 |
|                        | frankenphp |    — |    196 |   2.3 |   2.0 |  0.8 |  8.4 |  8.2 |      26 |
| Symfony API            | prefork    |    — |    388 |   4.1 |   3.4 |  2.2 | 12.1 |  9.1 |      36 |
|                        | frankenphp |    — |    369 |   3.1 |   2.5 |  1.6 | 11.0 |  8.5 |      31 |
| CakePHP API            | prefork    |    — |    192 |   3.6 |   3.3 |  2.2 | 12.2 |  9.3 |      36 |
|                        | frankenphp |    — |    183 |   2.9 |   2.6 |  1.8 | 10.9 |  8.6 |      31 |
| CodeIgniter API        | prefork    |    — |    195 |  10.0 |   7.5 |  6.2 | 17.1 | 14.1 |      67 |
|                        | frankenphp |    — |    189 |   7.8 |   7.0 |  6.3 | 16.3 | 13.2 |      64 |
| Laravel API, shared    | prefork    |    — |    220 |   6.1 |   5.6 |  3.3 | 11.9 | 10.7 |      48 |
|                        | frankenphp |    — |    211 |   5.4 |   4.9 |  2.9 | 10.8 |  9.9 |      43 |
| Symfony full-stack     | prefork    |  2.7 |    386 |   7.0 |   6.5 |  4.3 | 18.2 | 19.7 |      68 |
|                        | frankenphp |  2.4 |    377 |   6.0 |   5.9 |  4.3 | 16.4 | 16.9 |      60 |
| CakePHP full-stack     | prefork    |  2.4 |    201 |  10.5 |  10.1 |  3.7 | 19.1 | 18.4 |      82 |
|                        | frankenphp |  2.0 |    190 |   9.8 |   9.5 |  3.2 | 16.4 | 15.8 |      72 |
| CodeIgniter full-stack | prefork    |  5.7 |    229 |  17.1 |  17.4 |  8.2 | 30.3 | 37.7 |     144 |
|                        | frankenphp |  6.4 |    224 |  17.8 |  18.1 |  9.1 | 32.6 | 39.5 |     152 |
| Laravel full-stack, shared | prefork | 14.8 |   436 |  33.6 |  33.9 |  8.9 | 47.2 | 46.3 |     236 |
|                        | frankenphp | 13.5 |    420 |  33.0 |  33.8 |  8.0 | 46.7 | 46.2 |     235 |

Every step of every run, with p50, p99 and the CPU, is in [`benchmark/results.tsv`](../benchmark/results.tsv).

## Screenshots

Taken after each build's run: the login page and the product list, with
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
BENCH_JOURNEYS=10 BENCH_ONLY=gluo make benchmark                # a smoke run of one build; its database must be up
BENCH_CLIENTS=4 BENCH_JOURNEYS=100 benchmark/rounds.sh 3        # these results: about 1 h 30
python3 benchmark/median.py                                     # benchmark/results.tsv, from the rounds
```

`rounds.sh` starts each build's database, waits for it, runs the build, stops it, and keeps each
round in `benchmark/results-round<n>.tsv`. Run it on a quiet machine, and stop it from sleeping:
the `cpu` and `steal` columns help to spot a disturbed run.

| Variable          | Default                              |                                                                                   |
|-------------------|--------------------------------------|-----------------------------------------------------------------------------------|
| `BENCH_JOURNEYS`  | 3000                                 | journeys per run                                                                  |
| `BENCH_CLIENTS`   | 4                                    | concurrent clients, and PHP processes on each server                              |
| `BENCH_SUITE`     |                                      | `fullstack` or `api`; Gluo runs in both                                           |
| `BENCH_ONLY`      |                                      | only the targets whose name contains it (`cakephp`, `api-laravel`...)             |
| `BENCH_SERVERS`   | `fpm,worker,prefork,frankenphp`      | the servers to run, comma separated                                               |
| `BENCH_PHP_INI`   | `xdebug.mode=off`                    | PHP settings for every server, space separated (`name=value`); the image loads Xdebug |
| `BENCH_IMAGE`     | `byjg/php:8.5-fpm-nginx`             | the image of FPM and the gateway: php, php-fpm and nginx                          |
| `BENCH_GATEWAY`   |                                      | a gateway binary already built (default: `go build`)                              |
| `COMPAT_DIR`      | `../gluo-framework-comparison`       | the comparison repository (the compatibility tests read it too)                   |

The benchmark sets two more for FrankenPHP's worker script (`compat/frankenphp.php`):
`PHPWORKER_BOOT`, the boot file of `compat/` it runs, and `PHPWORKER_MAX_REQUESTS`, the build's
`-max-requests` (it ends the script after that many requests, and FrankenPHP starts it again).

The benchmark resets the databases before every run (see [The data](#the-journey)): it erases what
they had. It writes to the projects' ignored folders and databases: sessions (Laravel's `sessions`
table, CodeIgniter's `writable/session`), Passport's tokens (`oauth_access_tokens`) and logs. It also
rewrites the tracked `config/Migrations/schema-dump-default.lock` of both CakePHP builds: restore it
with `git checkout` afterwards.
