# Analysis: which server, for which framework

What the [benchmark](benchmark.md) shows: the CRUD journey on 5 frameworks, as APIs and as full
applications, on PHP-FPM, WarmPHP (with and without `-prefork`) and FrankenPHP (classic and worker
mode). All the
numbers are with OPcache on and 4 PHP workers unless it says otherwise.

Sentences marked **(judgment)** are not measured: they come from how each server works, not from
this benchmark.

## The short answer

In this four-worker CRUD benchmark, WarmPHP prefork improved median journey throughput by about 9 %
over FPM and reduced combined post-login latency by about 25 %, while using about 44 % more container
memory. Its observed performance was close to FrankenPHP's worker mode on several shared-application
workloads. These results do not establish production capacity or long-running reliability.

- **Is WarmPHP valid? With `-prefork`, yes, as a technical result.** Across the shared-application
  builds, its seven post-login operations were within 9 ms of FrankenPHP's combined time, and it
  beats FPM on whole journeys in 9 of 11 builds, without touching the application or the PHP build. It is not proven in production.
- **Highest observed throughput**: FrankenPHP's worker mode, in 10 of 11 configurations; prefork is
  level on full-stack CodeIgniter (10.1 against 10.0). Its lead over prefork goes from 2 % to 9 %,
  about 5 % at the median, and part of that is its PHP build.
- **The speed is the worker mode, not FrankenPHP**: in its classic mode, which runs the front
  controller on every request, FrankenPHP is FPM's speed, ×1.02 FPM's journeys/s at the median.
- **Second for speed: WarmPHP with `-prefork`**, ahead of FPM, FrankenPHP classic and the plain
  worker, and it runs the PHP you already have: a distribution's `php-cli` or any image. A PHP
  security fix arrives with your PHP packages, not with a server release; the gateway is a Go binary
  you keep up to date as well **(judgment)**. When memory decides, FPM is the second choice.
- **Least memory**: the two servers that boot on every request. FrankenPHP classic reads 2–5 MiB
  less than FPM in all 11 builds, but without nginx in its figure; with nginx estimated at 4
  processes, FPM is 7–11 MiB lower.
- **Without `-prefork`, the gateway is the fallback**: the same speed as pre-fork with a shared
  application, and ×1.7 to ×2.1 its memory. It is for a PHP without `pcntl`, or an application that
  does not survive a fork.
- **What decides most is not the server**: bcrypt is half or more of each journey, and a Laravel
  application with a new instance per request gives away the gain of any worker.

| | fpm | frankenphp-classic | worker | prefork | frankenphp |
|--------------------------------------------|-------:|-------:|-------:|-------:|-------:|
| Journeys/s against FPM (median, range)     |      — | ×1.02 (0.94–1.03) | ×1.09 (0.79–1.18) | ×1.09 (0.96–1.19) | ×1.15 (1.04–1.25) |
| The 7 steps, faster than FPM (median, range) | — | ×0.98 (0.89–1.02) | ×1.29 (0.57–1.92) | ×1.34 (0.90–2.00) | ×1.44 (1.04–2.31) |
| Memory against FPM (median, range)         |      — | ×0.95 (0.92–0.97) | ×2.85 (2.11–3.12) | ×1.44 (1.24–1.83) | ×1.11 (1.06–2.02) |
| Journeys that failed the checks, of 30,600 |      0 |      0 |      0 |      0 |      0 |
| Application changed                        |     no |     no |     no |     no |     no |
| One boot file per framework needed         |     no |     no |    yes |    yes |    yes |

## Best of

| Category                                    | Winner                                   | Numbers                                                                        |
|---------------------------------------------|------------------------------------------|--------------------------------------------------------------------------------|
| Most journeys/s                             | **CakePHP API on FrankenPHP**            | 18.0; Gluo 17.6; on prefork CakePHP 17.2, Gluo 16.7                            |
| Most journeys/s, FPM                        | **CakePHP API**                          | 15.8; Gluo 15.3, CodeIgniter API 14.5                                          |
| Best full application                       | **CakePHP**                              | 14.5 on FrankenPHP, 13.7 on prefork, 12.4 on FPM                               |
| Lowest combined latency (7 steps)           | **Gluo**                                 | 24 ms; CakePHP and Symfony APIs 30–37 on the workers; with FPM, Gluo 49, CakePHP 54 |
| Pages of 50 products                        | **Gluo**                                 | 1.9–2.6 ms; CakePHP and Symfony 2.5–4.1                                        |
| Read one product                            | **Gluo**                                 | 0.8–1.0 ms; 1.6–2.3 for Symfony and CakePHP                                    |
| Writes (edit + add)                         | **Gluo**                                 | 15 ms; CakePHP 18, Symfony 19, Laravel 20, CodeIgniter 28 (FrankenPHP)         |
| Least memory                                | **Gluo on FrankenPHP classic or FPM**    | 46 MiB without nginx, 48 with it; 50 on FrankenPHP, 59 on prefork              |
| Least damage without OPcache                | **Gluo**, among the FPM and classic runs | 9.3 journeys/s, 188 ms on FPM; the other APIs 3.7–7.2, 316–723 ms              |
| Largest gain from staying booted            | **Symfony API**                          | 71 → 31 ms on FrankenPHP (×2.3), 35 on prefork; Laravel API, shared, 94 → 44 (×2.1) |
| Smallest gain                               | **CodeIgniter**                          | API 74 → 60 ms at best (prefork), full-stack 193 → 152; full-stack Laravel 305 → 239 |
| Slowest                                     | **Laravel full-stack**                   | 5.8 journeys/s at best: it has no paging, so each list sends the whole table through Inertia |

CakePHP's API and Gluo are close at the top (18.0 against 17.6 on FrankenPHP, 17.2 against 16.7 on
prefork): CakePHP's login is faster (191 ms against 200 on FrankenPHP), and Gluo's 7 steps are faster
(24 ms against 30). Symfony comes last among the APIs in
journeys/s only because its hasher uses cost 13, twice the work: its 7 steps are in the top group.

## Each server

### PHP-FPM

**Strongest at: memory, and asking nothing.** One OPcache shared by all its children, and the
application freed after every request.

- **Measured, strong**: the least memory with nginx counted, 48–99 MiB; with nginx at 4 processes,
  not this machine's 22, about 35–87 MiB. Only FrankenPHP classic reads less, 2–5 MiB, without nginx.
  Steady on APIs: the slowest journey is 1.09–1.18 × p50.
- **Measured, weak**: with FrankenPHP classic, the slowest 7 steps in 9 of 11 builds (the plain
  worker is slower with Laravel's new application per request), which is consistent with booting on
  every request: Gluo's 7 steps take 49 ms against 24, Symfony's API 71 against 31. Without OPcache
  it falls apart, ×1.7 to ×3.3 fewer journeys/s, and so does FrankenPHP classic; no worker does.
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

- **Measured, strong**: FPM's speed, ×1.02 its journeys/s at the median (0.94–1.03): its login is
  about 3 % faster (its PHP build), and its 7 steps are the same, ×0.98 (0.89–1.02). The lowest memory
  reading in all 11 builds, 46–96 MiB, without nginx in it; with nginx estimated at 4 processes, FPM
  is 7–11 MiB lower. Steady: the slowest journey is 1.07–1.21 × p50.
- **Measured, weak**: what FPM loses, it loses: ×1.7 to ×3.3 fewer journeys/s without OPcache.
  Full-stack CodeIgniter is slower than on FPM in all 3 rounds, 8.2 against 8.8 journeys/s and
  217 against 193 ms in 7 steps; the cause was not measured.
- **The worker mode is FrankenPHP's gain**: on the same PHP build and server, the worker mode gives
  ×1.14 journeys/s at the median (1.06–1.22) and 7 steps ×1.78 faster (1.27–2.41), with a shared
  application, for ×1.17 the memory (1.11–1.45).
- **Pros (judgment)**: any application runs as on FPM, with no state crossing requests and no boot
  file. FrankenPHP's deployment: HTTPS, HTTP/2 and HTTP/3, one binary, no FastCGI.
- **Cons (judgment)**: FrankenPHP's, below: a ZTS PHP in its own image, and a PHP fix that comes
  when that is rebuilt; PHP inside the server process. And the boot is paid on every request, as on FPM.

**Use it** to move to FrankenPHP's deployment without changing how the application runs, or as the
step before its worker mode. **Avoid it** if you expect speed from it: it is FPM's.

### FrankenPHP, worker mode

**Strongest at: speed and simplicity of deployment.** A web server and PHP in one binary, with no
nginx and no extra hop.

- **Measured, strong**: the highest observed journeys/s in 10 of 11 builds, several of them
  near-ties (prefork is level on full-stack CodeIgniter, 10.1 against 10.0, and 2 % behind on
  CodeIgniter's API), the best 7 steps in 9 of 11 (not CodeIgniter's two builds), and close to flat
  with OPcache off. Memory close to FPM's on the simple builds: Gluo 50 against 48 MiB, with nginx not
  included, so it serves static files in that figure too.
- **Measured, weak**: Laravel with a new application per request costs twice FPM's memory (201 against
  99 MiB, full-stack), the largest ratio of any build. Part of its lead is not the server: its PHP
  build takes 5.6 % less time on bcrypt than the Alpine build the others use, and on Gluo the login
  is most of what separates it from prefork. Without nginx in its figure, its memory is not as
  far from prefork's as it looks: an estimate with nginx at 4 processes puts prefork at 47 against
  its 50 on Gluo, 58 against 60 on CodeIgniter's API and 94 against 100 on Laravel's.
- **Pros (judgment)**: an official Laravel integration through Octane, HTTP/2, HTTP/3 and HTTPS built
  in, one binary to ship, and an active project.
- **Cons (judgment)**: it requires a ZTS PHP, so its own image or build, and C extensions must be
  thread-safe. PHP is inside its image or binary, so a PHP security fix reaches you when that is
  rebuilt, by the project or by you. It can replace nginx, which changes how a team with an nginx setup runs; kept behind nginx, that process and its memory come back. PHP runs inside the
  server process, so a crash in an extension takes everything down. The application must be free of
  leaks across requests, as in any worker.

**Use it** for a new deployment, or one that can change its web server and image. **Avoid it** when
you depend on an extension that is not thread-safe, or your PHP image is fixed.

### WarmPHP with `-prefork`

**Strongest at: a worker's speed with what you already run.** The plain PHP CLI, NTS, from a
distribution package or any image, behind any proxy, close to FrankenPHP in speed and memory.

- **Measured, strong**: ×1.09 FPM's journeys/s and ×1.34 on the 7 steps at the median, up to ×2.00.
  The 7 steps are ahead of FrankenPHP on CodeIgniter's API (60 against 63) and full-stack
  CodeIgniter (152 against 160), 3 ms behind on Gluo, 5–6 ms on the Symfony and CakePHP builds and the
  Laravel API (7 to 10 requests, so under 1 ms each), and 8 ms behind on full-stack Laravel, shared,
  the largest gap with a shared application. It uses ×1.7 to ×2.1 less memory than the plain worker
  (Gluo 108 → 59 MiB, Laravel API 220 → 107), with no loss of speed. With OPcache off and a shared
  application, its journeys/s stay within 6 % of OPcache on, and its memory stays near FPM's on some
  builds (Gluo 62 against 58 MiB).
- **Measured, weak**: behind FrankenPHP in journeys/s: under 2 % on CodeIgniter, 5–7 % on the others,
  and about 9 % with Laravel's new application per request.
  More memory than FPM, ×1.24 to ×1.83, because the booted application's memory gets unshared as
  PHP's reference counts touch it. With a shared application, Symfony and CakePHP lose the most
  (CakePHP's API: 96 against 55 MiB).
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

### WarmPHP without `-prefork`

**Strongest at: simplicity of the process model.** Each worker is an independent `php` that boots by
itself.

- **Measured, strong**: the same speed as prefork in the shared-application builds, within the noise.
  With OPcache off it barely loses speed.
- **Measured, weak**: the most memory in all 11 builds, ×2.11 to ×3.12 FPM's, since each worker
  keeps its own OPcache and application. The worst with Laravel's new application per request: 9.7
  journeys/s against 11.8 on prefork, and the slowest journey at 2.12 × p50, which fits every `-max-requests` being a cold boot.
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
FrankenPHP classic, like FPM, boots on every request, and its rows are within a few percent of FPM's,
except full-stack CodeIgniter; its memory is the lowest only because nginx is not in its figure.

### Gluo

| Server             | journeys/s | 7 steps | memory |
|--------------------|-----------:|--------:|-------:|
| fpm                |       15.3 |      49 |     48 |
| frankenphp-classic |       15.6 |      50 | **46** |
| worker             |       16.5 |      28 |    108 |
| prefork            |       16.7 |      27 |     59 |
| frankenphp         |   **17.6** |  **24** |     50 |

The fastest 7 steps and the least memory on every server; in journeys/s, CakePHP's API is a little
ahead, with a faster login. The workers' lead over FPM, 21–25 ms in 7 steps, is consistent with avoiding the repeated boot.
Prefork is 3 ms behind FrankenPHP in those 7 steps: most of the difference in journeys/s is the login.

### Symfony

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |             8.3 |      71 |     61 |              7.1 |     129 |     68 |
| frankenphp-classic |             8.5 |      74 | **58** |              7.3 |     126 | **65** |
| worker             |             9.1 |      37 |    162 |              8.2 |      68 |    208 |
| prefork            |             9.1 |      35 |     84 |              8.1 |      67 |    116 |
| frankenphp         |         **9.6** |  **31** |     66 |          **8.7** |  **62** |     80 |

The clearest gain from staying booted: about half the time on any worker. Its low journeys/s come from
bcrypt at cost 13, which hides this. Worst on FPM without OPcache after Laravel: 509 ms in 7 steps, as an API.

### CakePHP

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |            15.8 |      54 |     55 |             12.4 |     107 |     61 |
| frankenphp-classic |            16.3 |      53 | **51** |             12.6 |     106 | **56** |
| worker             |            16.8 |      36 |    160 |             13.5 |      83 |    173 |
| prefork            |            17.2 |      35 |     96 |             13.7 |      80 |    104 |
| frankenphp         |        **18.0** |  **30** |     60 |         **14.5** |  **74** |     65 |

The best journeys/s, and the best full application. Here FrankenPHP is the clear pick over prefork:
faster, and 35–40 MiB less. This is prefork's worst memory result among the shared-application builds.

### CodeIgniter

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |            14.5 |      74 |     55 |              8.8 |     193 |     71 |
| frankenphp-classic |            14.4 |      81 | **52** |              8.2 |     217 | **67** |
| worker             |            15.0 |      62 |    126 |              9.6 |     161 |    150 |
| prefork            |            15.1 |  **60** |     71 |         **10.1** | **152** |     90 |
| frankenphp         |        **15.3** |      63 |     60 |             10.0 |     160 |     79 |

The smallest gain, all the workers are tied, and FrankenPHP classic is behind FPM. Most of its work happens on every request, and it
runs in `development`, as configured, with the debug toolbar writing a file per request. FPM with
OPcache is a sound choice here, unless the application is on `production` and measures otherwise.

### Laravel

Three ways to run it. FPM is the reference, "new app" builds an application per request, and "shared"
keeps one, with the resets of [`compat/laravel-shared.php`](../compat/laravel-shared.php).

| Server              | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|---------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                 |            12.2 |      94 |     74 |              5.1 |     305 |     99 |
| frankenphp-classic  |            12.5 |      95 | **69** |              5.1 |     310 | **96** |
| new app: worker     |             9.7 |     166 |    224 |              4.3 |     418 |    309 |
| new app: prefork    |            11.8 |     104 |    111 |              4.9 |     322 |    181 |
| new app: frankenphp |            12.9 |      88 |    109 |              5.3 |     293 |    201 |
| shared: worker      |            14.5 |      51 |    220 |              5.5 |     250 |    259 |
| shared: prefork     |            14.5 |      49 |    107 |              5.6 |     247 |    138 |
| shared: frankenphp  |        **15.3** |  **44** |    100 |          **5.8** | **239** |    131 |

- **A new application per request gains little**: at best 5 % over FPM's journeys/s, with more memory.
- **Shared halves the API's 7 steps** (94 → 44–51 ms), and its journeys/s go from 12.2 to 14.5–15.3;
  prefork is close to FrankenPHP on memory.
- **As a full application, the gain is small** (305 → 239–250 ms): the time goes to Inertia sending the
  whole table on every list, not to the boot, and the build is in `local`, with Inertia's DevTools
  recording each request.
- **The shared mode resets what the application in this benchmark needs**: session, authentication,
  array cache, queued cookies. It is a part of Octane: a real Laravel application should use Octane
  on FrankenPHP, or check [compatibility.md](compatibility.md) first.

## OPcache on a worker

Once booted, a worker has already compiled its code, so OPcache changes little with a shared
application: within 6 % in journeys/s, and mostly within 3 %. The exception is rebuilding the
application per request, as `compat/laravel-fresh.php` does, where it costs 9 to 13 % of the
journeys/s on prefork and FrankenPHP. FPM and FrankenPHP classic, which boot on every request, lose
×1.7 to ×3.3 of their journeys/s without it.

| Group                                | Journeys/s, OPcache on → off                                                                   |
|--------------------------------------|------------------------------------------------------------------------------------------------|
| APIs and Gluo, shared application    | Within 2 % on every worker                                                                     |
| Full-stack, shared application       | Within 3 %, except CakePHP on prefork (13.7 → 12.9, −6 %) and CodeIgniter on prefork and FrankenPHP (10.1 → 9.4, 10.0 → 9.4, −6 %); CodeIgniter's steps are slower without OPcache in all 3 rounds |
| Laravel, new application per request | API: prefork 11.8 → 10.3, FrankenPHP 12.9 → 11.2 (−13 %); full-stack: 4.9 → 4.4, 5.3 → 4.7 (−9 %, −11 %) |

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
| Gluo                       |  35 |                 46 |     96 |      47 |         50 |
| Symfony API                |  48 |                 58 |    150 |      71 |         66 |
| CakePHP API                |  43 |                 51 |    148 |      84 |         60 |
| CodeIgniter API            |  42 |                 52 |    113 |      58 |         60 |
| Laravel API, shared        |  62 |                 69 |    207 |      94 |        100 |
| Symfony full-stack         |  56 |                 65 |    196 |     104 |         80 |
| CakePHP full-stack         |  48 |                 56 |    160 |      92 |         65 |
| CodeIgniter full-stack     |  59 |                 67 |    138 |      77 |         79 |
| Laravel full-stack, shared |  87 |                 96 |    246 |     125 |        131 |

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
  FrankenPHP classic shows the effect on this journey: its login is about 3 % faster than FPM's.
- **No crash, leak or long-running test.** `-max-requests`, a worker replacing after a fatal error and
  timeouts are covered by the gateway's tests, not measured under load. Nothing here ran for days.
- **Static files were not part of it**: the journey skips the SPA's pages, so nginx's and Caddy's
  speed with them is not measured.
