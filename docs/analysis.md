# Analysis: which server, for which framework

What the [benchmark](benchmark.md) shows: the CRUD journey on 5 frameworks, as APIs and as full
applications, on PHP-FPM, WarmPHP (`-mode worker` and `-mode prefork`) and FrankenPHP (classic and worker
mode). All the
numbers are with OPcache on and 4 PHP workers unless it says otherwise.

Sentences marked **(judgment)** are not measured: they come from how each server works, not from
this benchmark.

## The short answer

In this four-worker CRUD benchmark, WarmPHP prefork improved median journey throughput by about 8 %
over FPM and reduced combined post-login latency by about 22 %, while using about 43 % more container
memory. Its observed performance was close to FrankenPHP's worker mode on most shared-application
workloads. These results do not establish production capacity or long-running reliability.

- **Is WarmPHP valid? With `-mode prefork`, yes, as a technical result.** In 8 of the 9
  shared-application builds, its seven post-login operations were within 8 ms of FrankenPHP's
  combined time (the exception is full-stack CakePHP, 20 ms), and it beats FPM on whole journeys in
  9 of 11 builds, without touching the application or the PHP build. It is not proven in production.
- **Highest observed throughput**: FrankenPHP's worker mode, in all 11 configurations; prefork is
  level on full-stack CodeIgniter (11.3 against 11.3). Its lead over prefork goes from 0 % to 12 %,
  about 6.5 % at the median, and part of that is its PHP build.
- **The speed is the worker mode, not FrankenPHP**: in its classic mode, which runs the front
  controller on every request, FrankenPHP is FPM's speed, ×1.01 FPM's journeys/s at the median.
- **Second for speed: WarmPHP with `-mode prefork`**, ahead of FPM, FrankenPHP classic and the plain
  worker, and it runs the PHP you already have: a distribution's `php-cli` or any image. A PHP
  security fix arrives with your PHP packages, not with a server release; the gateway is a Go binary
  you keep up to date as well **(judgment)**. When memory decides, FPM is the second choice.
- **Least memory**: the two servers that boot on every request. FrankenPHP classic reads 1–5 MiB
  less than FPM in all 11 builds, but without nginx in its figure; with nginx estimated at 4
  processes, FPM is 7–11 MiB lower.
- **With `-mode worker`, the gateway is the fallback**: the same speed as pre-fork with a shared
  application, and ×1.6 to ×2.1 its memory. It is for a PHP without `pcntl`, or an application that
  does not survive a fork.
- **What decides most is not the server**: bcrypt is half or more of each journey, and a Laravel
  application with a new instance per request gives away the gain of any worker.

| | fpm | frankenphp-classic | worker | prefork | frankenphp |
|--------------------------------------------|-------:|-------:|-------:|-------:|-------:|
| Journeys/s against FPM (median, range)     |      — | ×1.01 (1.00–1.04) | ×1.08 (0.80–1.18) | ×1.08 (0.97–1.18) | ×1.14 (1.04–1.26) |
| The 7 steps, faster than FPM (median, range) | — | ×0.97 (0.91–1.03) | ×1.24 (0.63–1.87) | ×1.29 (0.92–1.89) | ×1.46 (1.04–2.08) |
| Memory against FPM (median, range)         |      — | ×0.97 (0.91–0.98) | ×2.63 (1.99–3.09) | ×1.43 (1.23–1.85) | ×1.12 (1.06–2.09) |
| Journeys that failed the checks, of 30,600 |      0 |      0 |      0 |      0 |      0 |
| Application changed                        |     no |     no |     no |     no |     no |
| One boot file per framework needed         |     no |     no |    yes |    yes |    yes |

## Best of

| Category                                    | Winner                                   | Numbers                                                                        |
|---------------------------------------------|------------------------------------------|--------------------------------------------------------------------------------|
| Most journeys/s                             | **CakePHP API on FrankenPHP**            | 19.7; Gluo 19.5; on prefork CakePHP 18.5, Gluo 18.1                            |
| Most journeys/s, FPM                        | **CakePHP API**                          | 17.1; Gluo 16.8, CodeIgniter API 15.7                                          |
| Best full application                       | **CakePHP**                              | 16.4 on FrankenPHP, 14.6 on prefork, 13.9 on FPM                               |
| Lowest combined latency (7 steps)           | **Gluo**                                 | 23 ms; CakePHP and Symfony APIs 32–43 on the workers; with FPM, Gluo 42, CakePHP 53 |
| Pages of 50 products                        | **Gluo**                                 | 1.2–1.7 ms; CakePHP and Symfony 2.5–3.6                                        |
| Read one product                            | **Gluo**                                 | 0.7–0.9 ms; 1.5–1.8 for Symfony and CakePHP                                    |
| Writes (edit + add)                         | **Gluo**                                 | 17 ms; Laravel 21, CakePHP 22, Symfony 22, CodeIgniter 29 (the best of prefork and FrankenPHP) |
| Least memory                                | **Gluo on FrankenPHP classic or FPM**    | 44 MiB without nginx, 48 with it; 51 on FrankenPHP, 59 on prefork              |
| Least damage without OPcache                | **Gluo**, among the FPM and classic runs | 10.5 journeys/s, 164 ms on FPM; the other APIs 4.3–8.1, 277–641 ms             |
| Largest gain from staying booted            | **Laravel API, shared**                  | 87 → 42 ms on FrankenPHP (×2.1), 46 on prefork; Symfony API 65 → 35 on prefork (×1.9) |
| Smallest gain                               | **CodeIgniter**                          | API 70 → 61 ms at best (prefork), full-stack 175 → 136; full-stack Laravel 270 → 217 |
| Slowest                                     | **Laravel full-stack**                   | 6.3 journeys/s at best: it has no paging, so each list sends the whole table through Inertia |

CakePHP's API and Gluo are level at the top (19.7 against 19.5 on FrankenPHP, 18.5 against 18.1 on
prefork): CakePHP's login is faster (170 ms against 181 on FrankenPHP), and Gluo's 7 steps are faster
(23 ms against 32). Symfony comes last among the APIs in
journeys/s only because its hasher uses cost 13, twice the work: its 7 steps are in the top group.

## Each server

### PHP-FPM

**Strongest at: memory, and asking nothing.** One OPcache shared by all its children, and the
application freed after every request.

- **Measured, strong**: the least memory with nginx counted, 48–99 MiB; with nginx at 4 processes,
  not this machine's 22, about 35–86 MiB. Only FrankenPHP classic reads less, 1–5 MiB, without nginx.
  Steady: the slowest journey is 1.05–1.12 × p50, the narrowest of the five.
- **Measured, weak**: with FrankenPHP classic, the slowest 7 steps in 9 of 11 builds (the plain
  worker is slower with Laravel's new application per request), which is consistent with booting on
  every request: Gluo's 7 steps take 42 ms against 23, Symfony's API 65 against 35. Without OPcache
  it falls apart, ×1.6 to ×3.1 fewer journeys/s, and so does FrankenPHP classic; no worker does.
- **Pros (judgment)**: decades in production, every framework runs on it unchanged, no state can
  cross requests, and a bad request cannot corrupt the next one.
- **Cons (judgment)**: the boot is paid for ever, and it has to be tuned (`pm`, `max_children`). It
  needs FastCGI, so a proxy that does not speak it needs something in between.

**Use it** when memory is tight, when the code cannot be trusted to be free of leaks across requests,
or when the boot is cheap against the rest of the request. **Avoid it** when the framework's boot is
a large part of the response time and you pay for that CPU.

### FrankenPHP, classic mode

**Strongest at: FPM's model in one binary.** Caddy and PHP in one process, which runs the front
controller on every request, as FPM does: no boot file, no worker.

- **Measured, strong**: FPM's speed, ×1.01 its journeys/s at the median (1.00–1.04): its login is
  2–4 % faster (its PHP build), and its 7 steps are about the same, ×0.97 (0.91–1.03). The lowest memory
  reading in all 11 builds, 44–96 MiB, without nginx in it; with nginx estimated at 4 processes, FPM
  is 7–11 MiB lower. Steady: the slowest journey is 1.07–1.21 × p50.
- **Measured, weak**: what FPM loses, it loses: ×1.6 to ×3.2 fewer journeys/s without OPcache.
  Its 7 steps are up to 9 % slower than FPM's on Symfony (71 against 65 ms as an API, 118 against
  108 full-stack) and 7 % on CodeIgniter's API; the cause was not measured.
- **The worker mode is FrankenPHP's gain**: on the same PHP build and server, the worker mode gives
  ×1.13 journeys/s at the median (1.06–1.23) and 7 steps ×1.59 faster (1.21–2.08), with a shared
  application, for ×1.17 the memory (1.14–1.44).
- **Pros (judgment)**: any application runs as on FPM, with no state crossing requests and no boot
  file. FrankenPHP's deployment: HTTPS, HTTP/2 and HTTP/3, one binary, no FastCGI.
- **Cons (judgment)**: FrankenPHP's, below: a ZTS PHP in its own image, and a PHP fix that comes
  when that is rebuilt; PHP inside the server process. And the boot is paid on every request, as on FPM.

**Use it** to move to FrankenPHP's deployment without changing how the application runs, or as the
step before its worker mode. **Avoid it** if you expect speed from it: it is FPM's.

### FrankenPHP, worker mode

**Strongest at: speed and simplicity of deployment.** A web server and PHP in one binary, with no
nginx and no extra hop.

- **Measured, strong**: the highest observed journeys/s in all 11 builds, several of them
  near-ties (prefork is level on full-stack CodeIgniter, 11.3 against 11.3, and about 3 % behind on
  the Symfony and CodeIgniter APIs and full-stack Laravel, shared), the best 7 steps in 8 of 11 (not
  CodeIgniter's two builds, and not the Symfony API, where its `edit` step varied between rounds),
  and close to flat with OPcache off. Memory close to FPM's on the simple builds: Gluo 51 against
  48 MiB, with nginx not included, so it serves static files in that figure too.
- **Measured, weak**: Laravel with a new application per request costs twice FPM's memory (206 against
  99 MiB, full-stack), the largest ratio of any build. Part of its lead is not the server: its PHP
  build takes 5.6 % less time on bcrypt than the Alpine build the others use, and its login is
  faster in every build (181 against 194 ms on Gluo, against prefork). Without nginx in its figure, its memory is not as
  far from prefork's as it looks: an estimate with nginx at 4 processes puts prefork at 46 against
  its 51 on Gluo, 60 against 60 on CodeIgniter's API and 94 against 102 on Laravel's.
- **Pros (judgment)**: an official Laravel integration through Octane, HTTP/2, HTTP/3 and HTTPS built
  in, one binary to ship, and an active project.
- **Cons (judgment)**: it requires a ZTS PHP, so its own image or build, and C extensions must be
  thread-safe. PHP is inside its image or binary, so a PHP security fix reaches you when that is
  rebuilt, by the project or by you. It can replace nginx, which changes how a team with an nginx setup runs; kept behind nginx, that process and its memory come back. PHP runs inside the
  server process, so a crash in an extension takes everything down. The application must be free of
  leaks across requests, as in any worker.

**Use it** for a new deployment, or one that can change its web server and image. **Avoid it** when
you depend on an extension that is not thread-safe, or your PHP image is fixed.

### WarmPHP with `-mode prefork`

**Strongest at: a worker's speed with what you already run.** The plain PHP CLI, NTS, from a
distribution package or any image, behind any proxy, close to FrankenPHP in speed and memory.

- **Measured, strong**: ×1.08 FPM's journeys/s and ×1.29 on the 7 steps at the median, up to ×1.89.
  The 7 steps are ahead of FrankenPHP on CodeIgniter's API (61 against 62), full-stack CodeIgniter
  (136 against 140) and the Symfony API (35 against 43, FrankenPHP's `edit` step), 3–5 ms behind on
  Gluo, the CakePHP API and both shared Laravel builds (7 to 10 requests, so under 1 ms each), 8 ms
  on full-stack Symfony, and 20 ms on full-stack CakePHP, the largest gap with a shared application
  (its `edit` step, 26 against 17 ms, in the run with the widest spread, 11 %). It uses ×1.6 to ×2.1
  less memory than the plain worker (Gluo 108 → 59 MiB, Laravel API 221 → 107), with no loss of
  speed. With OPcache off and a shared application, its journeys/s stay within 6 % of OPcache on,
  and its memory stays near FPM's on some builds (Gluo 63 against 59 MiB).
- **Measured, weak**: behind FrankenPHP in journeys/s: under 4 % on CodeIgniter, the Symfony API and
  full-stack Laravel, shared, 6–8 % on Gluo, the CakePHP API, the shared Laravel API and full-stack
  Symfony, 12 % on full-stack CakePHP, and 8–10 % with Laravel's new application per request.
  More memory than FPM, ×1.23 to ×1.85, because the booted application's memory gets unshared as
  PHP's reference counts touch it. With a shared application, Symfony and CakePHP lose the most
  (CakePHP's API: 96 against 56 MiB).
- **Pros (judgment)**: no change to PHP, the SAPI or FPM. Works with any HTTP proxy, as a sidecar or
  in the same container. A replacement worker is a new fork, so it needs no boot. A worker past its
  timeout is killed, and the other requests go on. PHP stays in separate processes, so a crash takes
  down one worker. The gateway is a single static binary. PHP is yours, so a PHP security fix is a
  package or image update, with no WarmPHP release to wait for.
- **Cons (judgment)**: new, and no one has run it in production. The gateway is one more binary to
  keep up to date: it parses HTTP, and a fix in it or in Go needs a new build. It needs `pcntl` and
  `posix`, so Linux or macOS: Ubuntu's `php-cli` has both, Fedora's needs `php-process`, and the
  official `php` image needs `docker-php-ext-install pcntl`.
  A connection opened at boot would be shared by all the workers: it must be opened on the first
  request. It needs a boot file per framework, and `PhpWorker::input()` instead of `php://input`, and
  the compatibility is what `compat/` covers: Laravel's is a part of what Octane resets. If the master
  dies the gateway stops, and something must restart it. Two extra moving parts against FrankenPHP
  running alone: the proxy and the gateway.

**Use it** when you want a worker but keep your image, your PHP build and your proxy, or when a
non-thread-safe extension rules out FrankenPHP. **Avoid it**, for now, when you need a mature product
with a support history, or the framework's own worker runtime is available and you can use it.

### WarmPHP with `-mode worker`

**Strongest at: simplicity of the process model.** Each worker is an independent `php` that boots by
itself.

- **Measured, strong**: the same speed as prefork in the shared-application builds, within the noise.
  With OPcache off it barely loses speed.
- **Measured, weak**: the most memory in all 11 builds, ×1.99 to ×3.09 FPM's, since each worker
  keeps its own OPcache and application. The worst with Laravel's new application per request: 10.6
  journeys/s against 12.9 on prefork, and the slowest journey at 2.55 × p50, which fits every `-max-requests` being a cold boot.
- **Pros (judgment)**: needs no `pcntl`, so it is the mode for a PHP build without it. Boot-time
  connections are safe. There is no master to lose. It suits PHP run through another command, as
  `docker run`.
- **Cons (judgment)**: every replacement worker boots from zero, and all of the above on application
  state and on the boot file apply.

**Use it** only when pre-fork cannot be: no `pcntl`, or an application that does not survive a fork.
**Avoid it** in every other case.

### Side by side

| | fpm | frankenphp-classic | worker | prefork | frankenphp |
|------------------------------------|---------------|----------------|-------------------|-------------------|----------------|
| Boots the application              | every request | every request  | once per worker   | once              | once per worker |
| OPcache                            | shared        | shared         | one per worker    | shared            | shared         |
| Booted application in memory       | none          | none           | one per worker    | shared until written | one per worker |
| PHP build                          | any           | ZTS, its own   | any CLI           | CLI with `pcntl` and `posix` | ZTS, its own   |
| A PHP security fix comes from      | your PHP packages | its image or binary | your PHP packages | your PHP packages | its image or binary |
| Other parts to keep patched        | the FastCGI proxy | Caddy, inside it | the gateway and the proxy | the same | Caddy, inside it |
| Needs in front                     | FastCGI proxy | nothing        | HTTP proxy, for static files | the same | nothing |
| Request state can leak             | no            | no             | yes               | yes               | yes            |
| A crash takes down                 | one child     | possibly the server | one worker   | one worker; the master, all | possibly the server |
| Replacing a worker                 | a fork        | nothing to replace | a whole boot  | a fork            | re-running the worker script |
| Maturity                           | decades       | a few years    | new               | new               | a few years    |

## Each framework

Journeys/s, then the 7 steps in ms, then memory in MiB, with OPcache on. The best of each is in bold.
FrankenPHP classic, like FPM, boots on every request, and its rows are within a few percent of FPM's;
its memory is the lowest only because nginx is not in its figure.

### Gluo

| Server             | journeys/s | 7 steps | memory |
|--------------------|-----------:|--------:|-------:|
| fpm                |       16.8 |      42 |     48 |
| frankenphp-classic |       17.3 |      43 | **44** |
| worker             |       18.2 |      27 |    108 |
| prefork            |       18.1 |      26 |     59 |
| frankenphp         |   **19.5** |  **23** |     51 |

The fastest 7 steps and the least memory on every server; in journeys/s, CakePHP's API is level
(19.7 against 19.5), with a faster login. The workers' lead over FPM, 15–19 ms in 7 steps, is consistent with avoiding the repeated boot.
Prefork is 3 ms behind FrankenPHP in those 7 steps: most of the difference in journeys/s is the login.

### Symfony

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |             9.3 |      65 |     60 |              8.1 |     108 |     67 |
| frankenphp-classic |             9.4 |      71 | **59** |              8.2 |     118 | **66** |
| worker             |            10.1 |  **35** |    161 |              9.2 |      65 |    208 |
| prefork            |            10.1 |  **35** |     85 |              9.2 |      65 |    118 |
| frankenphp         |        **10.4** |      43 |     67 |          **9.8** |  **57** |     82 |

One of the clearest gains from staying booted: about half the time on any worker. Its low journeys/s come from
bcrypt at cost 13, which hides this. FrankenPHP's 43 ms as an API is its `edit` step, which took 11,
23 and 26 ms in the three rounds; without OPcache it reads 31. Worst on FPM without OPcache after
Laravel: 449 ms in 7 steps, as an API.

### CakePHP

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |            17.1 |      53 |     56 |             13.9 |      96 |     61 |
| frankenphp-classic |            17.8 |      52 | **51** |             14.2 |      98 | **56** |
| worker             |            18.5 |      36 |    161 |             15.0 |      77 |    173 |
| prefork            |            18.5 |      37 |     96 |             14.6 |      86 |    109 |
| frankenphp         |        **19.7** |  **32** |     62 |         **16.4** |  **66** |     65 |

The best journeys/s, and the best full application. Here FrankenPHP is the clear pick over prefork:
faster (12 % as a full application), and 34–44 MiB less. With Symfony, this is prefork's worst memory
result against FPM among the shared-application builds.

### CodeIgniter

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |            15.7 |      70 |     56 |              9.9 |     175 |     70 |
| frankenphp-classic |            15.8 |      75 | **51** |              9.9 |     180 | **68** |
| worker             |            16.2 |      63 |    127 |             11.0 |     141 |    149 |
| prefork            |            16.3 |  **61** |     72 |         **11.3** | **136** |     89 |
| frankenphp         |        **16.8** |      62 |     60 |         **11.3** |     140 |     79 |

The smallest gain, and all the workers are tied. Most of its work happens on every request, and it
runs in `development`, as configured, with the debug toolbar writing a file per request. FPM with
OPcache is a sound choice here, unless the application is on `production` and measures otherwise.

### Laravel

Three ways to run it. FPM is the reference, "new app" builds an application per request, and "shared"
keeps one, with the resets of [`compat/laravel-shared.php`](../compat/laravel-shared.php).

| Server              | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|---------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                 |            13.2 |      87 |     73 |              5.6 |     270 |     99 |
| frankenphp-classic  |            13.6 |      87 | **71** |              5.6 |     279 | **96** |
| new app: worker     |            10.6 |     137 |    146 |              4.7 |     360 |    244 |
| new app: prefork    |            12.9 |      94 |    104 |              5.4 |     294 |    182 |
| new app: frankenphp |            14.2 |      78 |    108 |              5.8 |     259 |    206 |
| shared: worker      |            15.6 |      47 |    221 |              6.1 |     226 |    259 |
| shared: prefork     |            15.7 |      46 |    107 |              6.1 |     220 |    137 |
| shared: frankenphp  |        **16.7** |  **42** |    102 |          **6.3** | **217** |    134 |

- **A new application per request gains little**: at best 8 % over FPM's journeys/s, with more memory,
  and the plain worker is slower than FPM.
- **Shared halves the API's 7 steps** (87 → 42–47 ms), and its journeys/s go from 13.2 to 15.6–16.7;
  prefork is close to FrankenPHP on memory.
- **As a full application, the gain is small** (270 → 217–226 ms): the time goes to Inertia sending the
  whole table on every list, not to the boot, and the build is in `local`, with Inertia's DevTools
  recording each request.
- **The shared mode resets what the application in this benchmark needs**: session, authentication,
  array cache, queued cookies. It is a part of Octane: a real Laravel application should use Octane
  on FrankenPHP, or check [compatibility.md](compatibility.md) first.

## OPcache on a worker

Once booted, a worker has already compiled its code, so OPcache changes little with a shared
application: within 6 % in journeys/s, and mostly within 2 %. The exception is rebuilding the
application per request, as `compat/laravel-fresh.php` does, where it costs 9 to 14 % of the
journeys/s on prefork and FrankenPHP. FPM and FrankenPHP classic, which boot on every request, lose
×1.6 to ×3.2 of their journeys/s without it.

| Group                                | Journeys/s, OPcache on → off                                                                   |
|--------------------------------------|------------------------------------------------------------------------------------------------|
| APIs and Gluo, shared application    | Within 2 % on every worker                                                                     |
| Full-stack, shared application       | Within 2 %, except CodeIgniter on prefork and FrankenPHP (11.3 → 10.7, −6 %; 11.3 → 10.9, −4 %) |
| Laravel, new application per request | API: prefork 12.9 → 11.1 (−14 %), FrankenPHP 14.2 → 12.4 (−12 %); full-stack: 5.4 → 4.9, 5.8 → 5.2 (−9 %, −10 %). The plain worker reads 4–6 % more without it (10.6 → 11.3, 4.7 → 4.9) |

Initial startup is excluded through warm-up. Worker recycling during the measured workload is
included: `-max-requests 30` in the Laravel new-application builds, the only ones that set it. Keep
OPcache on in production **(judgment)**: a plain worker compiles the code again each time it is
replaced, and the other builds do not measure that.

## Memory, compared fairly

Memory is one `docker stats` reading at the end of a run, not a peak or a steady state. FPM, worker
and prefork carry nginx in their figure, and FrankenPHP does not. `worker_processes auto`
starts 22 processes on this machine, 18.6 MiB; on a 4-core server it would be 6.3 MiB. An estimate,
subtracting the difference from the measured figures, as nginx was measured idle:

| Build                      | fpm | frankenphp-classic | worker | prefork | frankenphp |
|----------------------------|----:|-------------------:|-------:|--------:|-----------:|
| Gluo                       |  35 |                 44 |     96 |      46 |         51 |
| Symfony API                |  48 |                 59 |    149 |      73 |         67 |
| CakePHP API                |  43 |                 51 |    149 |      84 |         62 |
| CodeIgniter API            |  44 |                 51 |    114 |      60 |         60 |
| Laravel API, shared        |  61 |                 71 |    209 |      94 |        102 |
| Symfony full-stack         |  55 |                 66 |    196 |     106 |         82 |
| CakePHP full-stack         |  49 |                 56 |    160 |      97 |         65 |
| CodeIgniter full-stack     |  58 |                 68 |    137 |      77 |         79 |
| Laravel full-stack, shared |  86 |                 96 |    247 |     125 |        134 |

So FPM stays the lowest even in this estimate, and FrankenPHP classic, with no nginx to subtract,
is second. Between prefork and FrankenPHP the table is only
illustrative: it takes a run with `worker_processes 4` to name a winner. The per-process gap against FPM grows
with more workers, for all of them; the benchmark only ran 4.

## What this benchmark does not show

- **One machine, 4 clients and 4 PHP workers.** It does not show what happens above the workers'
  capacity, or with hundreds of connections.
- **The frameworks are as configured**: CodeIgniter in `development`, Laravel in `local`.
- **The runtime is not a neutral comparison**: FPM and the gateway run on Alpine's NTS PHP 8.5.12,
  FrankenPHP on Debian's ZTS 8.5.11. FrankenPHP's build is faster at bcrypt and
  md5, the other at a plain PHP loop: see [the CPU comparison](benchmark.md#how-each-server-runs).
  FrankenPHP classic shows the effect on this journey: its login is 2–4 % faster than FPM's.
- **No crash, leak or long-running test.** `-max-requests`, a worker replacing after a fatal error and
  timeouts are covered by the gateway's tests, not measured under load. Nothing here ran for days.
- **Static files were not part of it**: the journey skips the SPA's pages, so nginx's and Caddy's
  speed with them is not measured.
