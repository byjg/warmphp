# Benchmark: PHP-FPM, WarmPHP and FrankenPHP

The same user journey, run on the builds of
[gluo-framework-comparison](https://github.com/byjg/gluo-framework-comparison), by five servers:

- **fpm**: nginx and PHP-FPM, which boots the application on every request.
- **frankenphp-classic**: FrankenPHP without its worker mode: like FPM, it runs the project's front
  controller on every request, without nginx.
- **worker**: nginx and WarmPHP, one booted PHP process per worker.
- **prefork**: the same with `-mode prefork`: one boot, forked into the workers.
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

The boot files are the same for both suites, and for FrankenPHP's worker mode: they only need the project's root.
[docs/compatibility.md](compatibility.md) explains each one. No application is changed for the
benchmark.

## The journey

Every iteration is one visitor, with a new browser (cookies, connections): log in as the seeded
admin, then a whole CRUD session. A journey counts if every response passes the benchmark's checks: the status, the redirect to the
expected page, a list with products; anything else is an error. After each run, outside the timed
work, the database is checked too: each edited product has one of the prices sent for it, every new
product is there with its price, and nothing else appeared. A failed check fails the run.
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
- **prefork**: the same with `-mode prefork`; the workers are forks of one booted master, and talk to
  the gateway over a Unix socket.

The nginx configuration is the same for the three except that `location`: the image's defaults
(worker processes, mime types), no access log, `client_max_body_size 1m` (the gateway's
`-max-body`). The image's start scripts are not used: they listen on port 80 as the image's user,
and here nginx and PHP run as the user that owns the projects (they write their caches), on the
host network (the databases are on `127.0.0.1`). So the benchmark writes the FPM pool and the nginx
configuration itself, with their temporary files in a folder of its own.

**frankenphp** runs in its own image, [`benchmark/frankenphp/Dockerfile`](../benchmark/frankenphp/Dockerfile):
`dunglas/frankenphp:1-php8.5` (Debian, PHP 8.5.11 ZTS) with the extensions the projects need, and a
Caddyfile of its own: 4 PHP threads, no admin API, no HTTPS, no compression. Caddy also serves the
static files, so nginx is not there. Its worker script is [`compat/frankenphp.php`](../compat/frankenphp.php),
which runs the same boot files of `compat/`, with 4 workers.

**frankenphp-classic** is the same image and Caddyfile without the worker: `php_server` with the
project's front controller as its fallback (`try_files {path} /index.php`, `/app.php` for Gluo),
the files FPM runs. So it boots the application on every request, as FPM does, and its difference
from **frankenphp** is only the worker mode: the same PHP build, the same server, the same memory
reading without nginx.

**The two PHP builds are not equally fast**, which matters for the comparison. On this machine, in
the same CPU test, one process:

| PHP build                 | bcrypt, cost 12 | integer loop | md5 |
|---------------------------|----------------:|-------------:|----:|
| byjg (Alpine, NTS)        |        170.5 ms |       194 ms | 394 ms |
| FrankenPHP (Debian, ZTS)  |        161.0 ms |       209 ms | 379 ms |

So FrankenPHP's login is faster whatever the server does, in both its modes: 2 to 4 % against
FPM's in this benchmark (frankenphp-classic against fpm, which do the same work).

The **memory** column is what `docker stats` shows for that container after the run (without the
page cache): nginx, and PHP-FPM or the gateway with its workers; or FrankenPHP alone, in both modes. nginx with
this configuration and nothing behind it takes 18.6 MiB on this machine, because it starts one
process per CPU (22); with 4 processes, 6.3 MiB.

4 clients run the journeys concurrently, after a warm-up of 5 % of the journeys. The runs of a
build go in a random order, so no server is always the first after the database starts.

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

The gateway's logs are on in every `worker` and `prefork` run: a record per request, with PHP's
metrics. They cost about 40 to 70 µs per request. On Gluo's journey, measured apart with 300 journeys
and the median of 5 rounds, with the logs on and off, journeys/s did not change, and the 7 steps
took 1.1 to 1.9 % longer, within the difference between rounds.

100 journeys per run, one build at a time, with only that build's database up: 102 runs, 3 rounds
each, 30,600 journeys, **none failed the checks**. Each row is the round with the median journeys/s; the
`spread` column of [`benchmark/results.tsv`](../benchmark/results.tsv) is the difference between the rounds:
at most 11 %, and 1 % at the median, so treat a difference under about 5 % as too close to call; three short rounds are not a significance test. Each round is also in `benchmark/results-round<n>.tsv`.
Intel Core Ultra 9 185H (22 CPUs), 30 GB, Docker, every build on MySQL 8.4. All Docker containers
were removed before the first round, and the machine-wide `cpu` column reads 16 to 19 % in every
run. A journey's login is bcrypt, about half of it or more, so
the "7 steps" tables below show the frameworks and the servers better.

**A few rows vary more than the rest.** 92 of the 102 rows differ by 3 % or less between rounds. The
exceptions are full-stack CakePHP on prefork with OPcache, 11 % (14.6, 13.7 and 15.2 journeys/s),
the Symfony API on prefork with OPcache, 8 %, and the CakePHP API on classic FrankenPHP and the
Laravel API (shared application) on prefork, 7 %. Each of those is one run, not a whole round:
the other runs of the same rounds are steady. The step that moves is `edit`, the first write (full-stack
CakePHP on prefork took 26, 50 and 17 ms for it; the Symfony API on prefork, 12, 12 and 42), and the
same step varies inside rows whose journeys/s do not: the Symfony API on FrankenPHP with
OPcache took 11, 23 and 26 ms for it in the three rounds, so its 43 ms for the 7 steps, against
35 on prefork and 31 on FrankenPHP without OPcache, is that step, not the server.

### Journeys per second (higher is better)

**api**

| Build               | OPcache on: fpm | frankenphp-classic | worker | prefork | frankenphp | OPcache off: fpm | frankenphp-classic | worker | prefork | frankenphp |
|---------------------|----------------:|-------------------:|-------:|--------:|-----------:|-----------------:|-------------------:|-------:|--------:|-----------:|
| Gluo                |            16.8 |               17.3 |   18.2 |    18.1 |       19.5 |             10.5 |               10.7 |   18.1 |    18.1 |       19.4 |
| Symfony             |             9.3 |                9.4 |   10.1 |    10.1 |       10.4 |              4.6 |                4.6 |   10.1 |    10.1 |       10.7 |
| CakePHP             |            17.1 |               17.8 |   18.5 |    18.5 |       19.7 |              7.7 |                7.8 |   18.4 |    18.4 |       19.7 |
| CodeIgniter         |            15.7 |               15.8 |   16.2 |    16.3 |       16.8 |              8.1 |                8.1 |   16.1 |    16.1 |       16.6 |
| Laravel, new app    |            13.2 |               13.6 |   10.6 |    12.9 |       14.2 |              4.3 |                4.3 |   11.3 |    11.1 |       12.4 |
| Laravel, shared app |            13.2 |               13.6 |   15.6 |    15.7 |       16.7 |              4.3 |                4.3 |   15.7 |    15.6 |       16.5 |

**fullstack** (Gluo is the API above)

| Build               | OPcache on: fpm | frankenphp-classic | worker | prefork | frankenphp | OPcache off: fpm | frankenphp-classic | worker | prefork | frankenphp |
|---------------------|----------------:|-------------------:|-------:|--------:|-----------:|-----------------:|-------------------:|-------:|--------:|-----------:|
| Symfony             |             8.1 |                8.2 |    9.2 |     9.2 |        9.8 |              3.1 |                3.1 |    9.1 |     9.1 |        9.7 |
| CakePHP             |            13.9 |               14.2 |   15.0 |    14.6 |       16.4 |              5.3 |                5.1 |   14.8 |    14.8 |       16.1 |
| CodeIgniter         |             9.9 |                9.9 |   11.0 |    11.3 |       11.3 |              4.7 |                4.7 |   10.7 |    10.7 |       10.9 |
| Laravel, new app    |             5.6 |                5.6 |    4.7 |     5.4 |        5.8 |              2.3 |                2.3 |    4.9 |     4.9 |        5.2 |
| Laravel, shared app |             5.6 |                5.6 |    6.1 |     6.1 |        6.3 |              2.3 |                2.3 |    6.0 |     6.0 |        6.2 |

### The 7 steps after the login, ms (lower is better)

The combined latency of the seven operations after the login: the sum of the average time of list1
to relist2, as the client sees it, without bcrypt. It includes HTTP, the database, the response and,
in the full-stack suite, the redirects and the new-product form: 9 or 10 requests there, 7 in the API
suite.

| Build                       | OPcache on: fpm | frankenphp-classic | worker | prefork | frankenphp | OPcache off: fpm | frankenphp-classic | worker | prefork | frankenphp |
|-----------------------------|----------------:|-------------------:|-------:|--------:|-----------:|-----------------:|-------------------:|-------:|--------:|-----------:|
| Gluo                        |              42 |                 43 |     27 |      26 |         23 |              164 |                163 |     26 |      27 |         24 |
| Symfony API                 |              65 |                 71 |     35 |      35 |         43 |              449 |                449 |     35 |      35 |         31 |
| CakePHP API                 |              53 |                 52 |     36 |      37 |         32 |              301 |                301 |     37 |      38 |         33 |
| CodeIgniter API             |              70 |                 75 |     63 |      61 |         62 |              277 |                282 |     62 |      64 |         64 |
| Laravel API, new app        |              87 |                 87 |    137 |      94 |         78 |              641 |                634 |    130 |     135 |        110 |
| Laravel API, shared         |              87 |                 87 |     47 |      46 |         42 |              641 |                634 |     46 |      47 |         44 |
| Symfony full-stack          |             108 |                118 |     65 |      65 |         57 |              743 |                750 |     67 |      67 |         60 |
| CakePHP full-stack          |              96 |                 98 |     77 |      86 |         66 |              467 |                474 |     79 |      79 |         71 |
| CodeIgniter full-stack      |             175 |                180 |    141 |     136 |        140 |              511 |                524 |    147 |     150 |        150 |
| Laravel full-stack, new app |             270 |                279 |    360 |     294 |        259 |             1040 |               1037 |    352 |     356 |        319 |
| Laravel full-stack, shared  |             270 |                279 |    226 |     220 |        217 |             1040 |               1037 |    230 |     230 |        228 |

### Memory, MiB (lower is better)

The whole container, with 4 PHP workers: one `docker stats` reading at the end of the run, not a
peak or a steady state. FPM, worker and prefork include nginx (18.6 MiB here);
FrankenPHP, in both modes, is alone.

| Build                       | OPcache on: fpm | frankenphp-classic | worker | prefork | frankenphp | OPcache off: fpm | frankenphp-classic | worker | prefork | frankenphp |
|-----------------------------|----------------:|-------------------:|-------:|--------:|-----------:|-----------------:|-------------------:|-------:|--------:|-----------:|
| Gluo                        |              48 |                 44 |    108 |      59 |         51 |               59 |                 55 |     92 |      63 |         57 |
| Symfony API                 |              60 |                 59 |    161 |      85 |         67 |               97 |                 93 |    142 |     112 |         99 |
| CakePHP API                 |              56 |                 51 |    161 |      96 |         62 |               76 |                 75 |    142 |     110 |         78 |
| CodeIgniter API             |              56 |                 51 |    127 |      72 |         60 |               76 |                 72 |    108 |      80 |         74 |
| Laravel API, new app        |              73 |                 71 |    146 |     104 |        108 |              143 |                139 |    114 |     166 |        179 |
| Laravel API, shared         |              73 |                 71 |    221 |     107 |        102 |              143 |                139 |    181 |     114 |        146 |
| Symfony full-stack          |              67 |                 66 |    208 |     118 |         82 |              106 |                101 |    184 |     152 |        125 |
| CakePHP full-stack          |              61 |                 56 |    173 |     109 |         65 |               85 |                 75 |    152 |     120 |         89 |
| CodeIgniter full-stack      |              70 |                 68 |    149 |      89 |         79 |               91 |                 87 |    134 |     104 |        101 |
| Laravel full-stack, new app |              99 |                 96 |    244 |     182 |        206 |              170 |                167 |    275 |     243 |        294 |
| Laravel full-stack, shared  |              99 |                 96 |    259 |     137 |        134 |              170 |                167 |    219 |     149 |        184 |

Laravel with a new application per request is measured at whatever point `-max-requests 30` left it,
so its memory moves between rounds (146 to 218 MiB, API worker with OPcache; 91 to 112 MiB on
prefork).

### Each step, OPcache on

Prefork and FrankenPHP, in ms. "page" is the login page, `list1` and `list2` are 50 products.

| Build                      | Server     | page | log in | list1 | list2 | get | edit |  add | 7 steps |
|----------------------------|------------|-----:|-------:|------:|------:|----:|-----:|-----:|--------:|
| Gluo                       | prefork    |    — |    194 |   1.7 |   1.6 | 0.9 |  9.5 |  9.2 |      26 |
|                            | frankenphp |    — |    181 |   1.4 |   1.2 | 0.7 |  8.8 |  8.7 |      23 |
| Symfony API                | prefork    |    — |    361 |   3.2 |   2.9 | 1.8 | 12.3 |  9.8 |      35 |
|                            | frankenphp |    — |    341 |   2.7 |   2.5 | 1.6 | 23.3 |  8.8 |      43 |
| CakePHP API                | prefork    |    — |    179 |   3.5 |   3.0 | 1.8 | 14.0 | 10.4 |      37 |
|                            | frankenphp |    — |    170 |   2.7 |   2.5 | 1.5 | 12.0 |  9.6 |      32 |
| CodeIgniter API            | prefork    |    — |    185 |   7.5 |   6.5 | 5.3 | 16.9 | 13.7 |      61 |
|                            | frankenphp |    — |    176 |   8.3 |   6.6 | 5.9 | 15.9 | 13.4 |      62 |
| Laravel API, shared        | prefork    |    — |    209 |   5.4 |   5.3 | 3.1 | 11.2 | 11.3 |      46 |
|                            | frankenphp |    — |    197 |   4.9 |   4.5 | 2.6 | 10.5 | 10.2 |      42 |
| Symfony full-stack         | prefork    |  2.4 |    367 |   6.6 |   6.1 | 4.1 | 17.7 | 18.4 |      65 |
|                            | frankenphp |  2.1 |    347 |   5.2 |   5.0 | 3.5 | 16.8 | 16.4 |      57 |
| CakePHP full-stack         | prefork    |  2.3 |    186 |   9.7 |   9.2 | 3.1 | 26.1 | 19.2 |      86 |
|                            | frankenphp |  1.8 |    176 |   8.2 |   7.8 | 2.7 | 16.6 | 15.0 |      66 |
| CodeIgniter full-stack     | prefork    |  5.5 |    212 |  16.6 |  15.9 | 7.1 | 28.6 | 35.5 |     136 |
|                            | frankenphp |  5.8 |    206 |  16.3 |  16.4 | 7.9 | 30.5 | 37.1 |     140 |
| Laravel full-stack, shared | prefork    | 14.6 |    417 |  30.1 |  30.1 | 9.2 | 44.4 | 44.6 |     220 |
|                            | frankenphp | 13.3 |    404 |  29.8 |  30.0 | 8.2 | 43.0 | 43.5 |     217 |

Every step of every run, with p50, p99 and the CPU, is in [`benchmark/results.tsv`](../benchmark/results.tsv).
With 100 journeys, its `p99` is the slowest journey.

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
BENCH_CLIENTS=4 BENCH_JOURNEYS=100 benchmark/rounds.sh 3        # these results: about 1 h 45
python3 benchmark/median.py                                     # benchmark/results.tsv, from the rounds
```

`rounds.sh` erases the earlier rounds, starts each build's database, waits for it, runs the build,
stops it, and keeps each round in `benchmark/results-round<n>.tsv`. It stops at the first failure,
and `median.py` refuses rounds that are not complete. Run it on a quiet machine, and stop it from sleeping:
the `cpu` and `steal` columns help to spot a disturbed run.

| Variable          | Default                              |                                                                                   |
|-------------------|--------------------------------------|-----------------------------------------------------------------------------------|
| `BENCH_JOURNEYS`  | 3000                                 | journeys per run                                                                  |
| `BENCH_CLIENTS`   | 4                                    | concurrent clients, and PHP processes on each server                              |
| `BENCH_SUITE`     |                                      | `fullstack` or `api`; Gluo runs in both                                           |
| `BENCH_ONLY`      |                                      | only the targets whose name contains it (`cakephp`, `api-laravel`...)             |
| `BENCH_SERVERS`   | `fpm,frankenphp-classic,worker,prefork,frankenphp` | the servers to run, comma separated                                 |
| `BENCH_PHP_INI`   | `xdebug.mode=off`                    | PHP settings for every server, space separated (`name=value`); the image loads Xdebug |
| `BENCH_IMAGE`     | `byjg/php:8.5-fpm-nginx`             | the image of FPM and the gateway: php, php-fpm and nginx                          |
| `BENCH_GATEWAY`   |                                      | a gateway binary already built (default: `go build`)                              |
| `COMPAT_DIR`      | `../gluo-framework-comparison`       | the comparison repository (the compatibility tests read it too)                   |

The benchmark sets two more for FrankenPHP's worker script (`compat/frankenphp.php`):
`WARMPHP_BOOT`, the boot file of `compat/` it runs, and `WARMPHP_MAX_REQUESTS`, the build's
`-max-requests` (it ends the script after that many requests, and FrankenPHP starts it again).

The benchmark resets the databases before every run (see [The data](#the-journey)): it erases what
they had. It writes to the projects' ignored folders and databases: sessions (Laravel's `sessions`
table, CodeIgniter's `writable/session`), Passport's tokens (`oauth_access_tokens`) and logs. It also
rewrites the tracked `config/Migrations/schema-dump-default.lock` of both CakePHP builds: restore it
with `git checkout` afterwards.
