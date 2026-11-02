# Analysis: which server, for which framework

What the [benchmark](benchmark.md) shows: the CRUD journey on 5 frameworks, as APIs and as full
applications, on PHP-FPM, phpworker-gateway (with and without `-prefork`) and FrankenPHP. All the
numbers are with OPcache on and 4 PHP processes unless it says otherwise.

Sentences marked **(judgment)** are not measured: they come from how each server works, not from
this benchmark.

## The short answer

- **Is phpworker-gateway valid? With `-prefork`, yes, as a technical result.** It does the framework's
  work as fast as FrankenPHP, or within 1.5 ms per request with a shared application, and beats FPM
  on whole journeys in 9 of 11 builds, without touching the application or the PHP build. It is not proven in production.
- **Fastest**: FrankenPHP, in 10 of 11 builds, usually 2–8 % over pre-fork (11 % with Laravel's new
  application per request), and part of that is its PHP build.
- **Least memory**: FPM, in all 11 builds.
- **Without `-prefork`, the gateway has no case**: same speed as pre-fork, with ×1.6 to ×2.3 its memory.
- **What decides most is not the server**: bcrypt is half or more of each journey, and a Laravel
  application with a new instance per request gives away the gain of any worker.

| | fpm | worker | prefork | frankenphp |
|--------------------------------------------|-------:|-------:|-------:|-------:|
| Journeys/s against FPM (median, range)     |      — | ×1.08 (0.77–1.21) | ×1.09 (0.94–1.19) | ×1.14 (1.00–1.25) |
| The 7 steps, faster than FPM (median, range) | — | ×1.48 (0.58–1.90) | ×1.52 (0.88–1.95) | ×1.72 (1.01–2.28) |
| Memory against FPM (median, range)         |      — | ×2.83 (2.18–3.14) | ×1.38 (1.25–1.81) | ×1.18 (1.05–2.03) |
| Failed journeys, of 25,200                 |      0 |      0 |      0 |      0 |
| Application changed                        |     no |     no |     no |     no |
| One boot file per framework needed         |     no |    yes |    yes |    yes |

## Best of

| Category                                    | Winner                                   | Numbers                                                                        |
|---------------------------------------------|------------------------------------------|--------------------------------------------------------------------------------|
| Most journeys/s                             | **CakePHP API on FrankenPHP**            | 18.5; Gluo 18.0; CakePHP's prefork 17.5, FPM 16.2                              |
| Most journeys/s, FPM                        | **CakePHP API**                          | 16.2; Gluo 15.7, CodeIgniter API 14.9                                          |
| Best full application                       | **CakePHP**                              | 15.1 on FrankenPHP, 14.0 on prefork, 12.1 on FPM                               |
| Fastest framework code (7 steps)            | **Gluo**                                 | 26 ms; Symfony and CakePHP APIs 31–37; with FPM, Gluo 48, CakePHP 54           |
| Pages of 50 products                        | **Gluo**                                 | 2.0–2.7 ms; CakePHP and Symfony 2.5–4.1                                        |
| Read one product                            | **Gluo**                                 | 0.8–1.0 ms; 1.6–2.2 for Symfony and CakePHP                                    |
| Writes (edit + add)                         | **Gluo**                                 | 16 ms; Symfony and CakePHP about 20; Laravel 21; CodeIgniter 30                |
| Least memory                                | **Gluo on FPM**                          | 47 MiB; 49 on FrankenPHP, 59 on prefork                                        |
| Least damage without OPcache                | **Gluo**, among the FPM runs             | 9.6 journeys/s, 184 ms; the others 3.9–7.4, 304–707 ms                         |
| Largest gain from staying booted            | **Symfony API**                          | 71 → 31 ms on FrankenPHP (×2.3), 36 on prefork; Laravel API shared 93 → 43     |
| Smallest gain                               | **CodeIgniter**                          | 74 → 64 ms as an API; most of its work happens on every request                |
| Slowest                                     | **Laravel full-stack**                   | 5.9 journeys/s at best: it has no paging, so each list sends the whole table through Inertia |

Gluo and CakePHP are tied at the top: what separates their journeys/s is mostly the login (196 ms
against 183 on FrankenPHP), and Gluo's 7 steps are faster. Symfony comes last among the APIs in
journeys/s only because its hasher uses cost 13, twice the work: its 7 steps are in the top group.

## Each server

### PHP-FPM

**Strongest at: memory, and asking nothing.** One OPcache shared by all its children, and the
application freed after every request.

- **Measured, strong**: the least memory in all 11 builds, 47–99 MiB. With nginx at 4 processes, not
  this machine's 22, that is about 35–87 MiB. Steady on APIs: p99 is 1.07–1.19 × p50.
- **Measured, weak**: the slowest for real work in 9 of 11 builds. Each request pays for the boot:
  Gluo's 7 steps take 48 ms against 26, Symfony's API 71 against 31. Without OPcache it falls
  apart, ×1.8 to ×3.8 fewer journeys/s, and it is the only server that does. On full-stack Symfony
  and CakePHP its p99 reached 1.85 × p50, and CakePHP moved 14 % between rounds. Those are the first
  runs after the database starts, which may be the reason; it was not established.
- **Pros (judgment)**: decades in production, every framework runs on it unchanged, no state can
  cross requests, and a bad request cannot corrupt the next one.
- **Cons (judgment)**: the boot is paid for ever, and it has to be tuned (`pm`, `max_children`). It
  needs FastCGI, so a proxy that does not speak it needs something in between.

**Use it** when memory is tight, when the code cannot be trusted to be free of leaks across requests,
or when the boot is cheap against the rest of the request. **Avoid it** when the framework's boot is
a large part of the response time and you pay for that CPU.

### FrankenPHP

**Strongest at: speed and simplicity of deployment.** A web server and PHP in one binary, with no
nginx and no extra hop.

- **Measured, strong**: the most journeys/s in 10 of 11 builds (prefork beats it on full-stack CodeIgniter,
  and FPM ties it on full-stack Laravel, new app), the best 7 steps in 10 of 11, and nearly flat
  with OPcache off. Memory close to FPM's on the simple builds: Gluo 49 against 47 MiB, with nginx not
  included, so it serves static files in that figure too.
- **Measured, weak**: Laravel with a new application per request costs twice FPM's memory (201 against
  99 MiB, full-stack), the largest ratio of any build. Part of its lead is not the server: its PHP
  build takes 5.6 % less time on bcrypt than the Alpine build the others use, and on Gluo the login
  is almost all of what separates it from prefork. Without nginx, the memory is not as far from
  prefork as it looks: with nginx at 4 processes, both are about 47–49 MiB on Gluo, and prefork uses
  less on CodeIgniter's API (57 against 60) and Laravel's (93 against 101).
- **Pros (judgment)**: an official Laravel integration through Octane, HTTP/2, HTTP/3 and HTTPS built
  in, one binary to ship, and an active project.
- **Cons (judgment)**: it requires a ZTS PHP, so its own image or build, and C extensions must be
  thread-safe. It replaces nginx: a team with an nginx setup changes how it runs. PHP runs inside the
  server process, so a crash in an extension takes everything down. The application must be free of
  leaks across requests, as in any worker.

**Use it** for a new deployment, or one that can change its web server and image. **Avoid it** when
you depend on an extension that is not thread-safe, or your PHP image and nginx are fixed.

### phpworker-gateway with `-prefork`

**Strongest at: a worker's speed with what you already run.** The plain PHP CLI, NTS, from any image,
behind any proxy, close to FrankenPHP in speed and memory.

- **Measured, strong**: ×1.09 FPM's journeys/s and ×1.52 on the 7 steps at the median, up to ×1.95.
  The 7 steps are level with FrankenPHP on Gluo (26 ms), and ahead on full-stack CodeIgniter (144
  against 152); 5 ms behind on the Symfony and CakePHP APIs, for 7 requests, so about 0.7 ms per
  request, and 10 ms behind on full-stack CakePHP, its largest gap with a shared application. It uses
  ×1.6 to ×2.3 less memory than the plain worker (Gluo 109 → 59 MiB, Laravel API 221 → 106), with no
  loss of speed. With OPcache off, its memory stays near FPM's on some builds and it is
  as fast as with OPcache on.
- **Measured, weak**: behind FrankenPHP in journeys/s, except on full-stack CodeIgniter: 2–8 %, and 11 %
  with Laravel's new application per request.
  More memory than FPM, ×1.25 to ×1.81, because the booted application's memory gets unshared as
  PHP's reference counts touch it. CakePHP loses the most (96 against 53 MiB).
- **Pros (judgment)**: no change to PHP, the SAPI or FPM. Works with any HTTP proxy, as a sidecar or
  in the same container. A replacement worker is a new fork, so it needs no boot. A worker past its
  timeout is killed, and the other requests go on. PHP stays in separate processes, so a crash takes
  down one worker. The gateway is a single static binary.
- **Cons (judgment)**: new, and no one has run it in production. It needs `pcntl`, so Linux or macOS.
  A connection opened at boot would be shared by all the workers: it must be opened on the first
  request. It needs a boot file per framework, and `PhpWorker::input()` instead of `php://input`, and
  the compatibility is what `compat/` covers: Laravel's is a part of what Octane resets. If the master
  dies the gateway stops, and something must restart it. Two extra moving parts against FrankenPHP:
  the proxy and the gateway.

**Use it** when you want a worker but keep your image, your PHP build and your proxy, or when a
non-thread-safe extension rules out FrankenPHP. **Avoid it**, for now, when you need a mature product
with a support history, or the framework's own worker runtime is available and you can use it.

### phpworker-gateway without `-prefork`

**Strongest at: simplicity of the process model.** Each worker is an independent `php` that boots by
itself.

- **Measured, strong**: the same speed as prefork in the shared-application builds, within the noise.
  With OPcache off it barely loses speed.
- **Measured, weak**: the most memory in all 11 builds, ×2.18 to ×3.14 FPM's, since each worker
  keeps its own OPcache and application. The worst with Laravel's new application per request: 9.7
  journeys/s against 12.0 on prefork, and p99 at 2.16 × p50, as every `-max-requests` is a cold boot.
- **Pros (judgment)**: needs no `pcntl`, so it is the mode for a PHP build without it. Boot-time
  connections are safe. There is no master to lose. It suits PHP run through another command, as
  `docker run`.
- **Cons (judgment)**: every replacement worker boots from zero, and all of the above on application
  state and on the boot file apply.

**Use it** only when pre-fork cannot be: no `pcntl`, or an application that does not survive a fork.
**Avoid it** in every other case.

### Side by side

| | fpm | worker | prefork | frankenphp |
|------------------------------------|---------------|-------------------|-------------------|----------------|
| Boots the application              | every request | once per worker   | once              | once per worker |
| OPcache                            | shared        | one per worker    | shared            | shared         |
| Booted application in memory       | none          | one per worker    | shared until written | one per worker |
| PHP build                          | any           | any CLI           | CLI with `pcntl`  | ZTS, its own   |
| Needs in front                     | FastCGI proxy | HTTP proxy, for static files | the same | nothing |
| Request state can leak             | no            | yes               | yes               | yes            |
| A crash takes down                 | one child     | one worker        | one worker; the master, all | possibly the server |
| Replacing a worker                 | a fork        | a whole boot      | a fork            | re-running the worker script |
| Maturity                           | decades       | new               | new               | a few years    |

## Each framework

Journeys/s, then the 7 steps in ms, then memory in MiB, with OPcache on. The best of each is in bold.

### Gluo

| Server     | journeys/s | 7 steps | memory |
|------------|-----------:|--------:|-------:|
| fpm        |       15.7 |      48 |   **47** |
| worker     |       17.0 |      28 |    109 |
| prefork    |       17.1 |    **26** |     59 |
| frankenphp |     **18.0** |    **26** |     49 |

The fastest and smallest framework here on any server. FPM's gap is only the boot, 22 ms in 7 steps.
Prefork and FrankenPHP do the same work in the same time: the difference in journeys/s is the login.

### Symfony

| Server     | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm        |             8.5 |      71 |   **61** |              7.3 |     129 |   **67** |
| worker     |             9.3 |      37 |    162 |              8.8 |      69 |    208 |
| prefork    |             9.4 |      36 |     84 |              8.7 |      68 |    116 |
| frankenphp |           **9.9** |    **31** |     68 |            **9.0** |    **60** |     82 |

The clearest gain from staying booted: about half the time on any worker. Its low journeys/s come from
bcrypt at cost 13, which hides this. Worst on FPM without OPcache after Laravel: 494 ms in 7 steps.

### CakePHP

| Server     | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm        |            16.2 |      54 |   **53** |             12.1 |     126 |   **61** |
| worker     |            16.9 |      37 |    159 |             14.2 |      82 |    172 |
| prefork    |            17.5 |      36 |     96 |             14.0 |      82 |    106 |
| frankenphp |          **18.5** |    **31** |     63 |           **15.1** |    **72** |     69 |

The best journeys/s, and the best full application. Here FrankenPHP is the clear pick over prefork:
faster, and 30–40 MiB less. This is prefork's worst memory result among the shared-application builds.

### CodeIgniter

| Server     | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm        |            14.9 |      74 |   **55** |              9.4 |     183 |   **68** |
| worker     |            15.3 |      64 |    127 |             10.3 |     151 |    149 |
| prefork    |            15.2 |      67 |     70 |           **10.5** |   **144** |     89 |
| frankenphp |          **15.7** |    **64** |     60 |             10.4 |     152 |     77 |

The smallest gain, and all the workers are tied. Most of its work happens on every request, and it
runs in `development`, as configured, with the debug toolbar writing a file per request. FPM with
OPcache is a sound choice here, unless the application is on `production` and measures otherwise.

### Laravel

Three ways to run it. FPM is the reference, "new app" builds an application per request, and "shared"
keeps one, with the resets of [`compat/laravel-shared.php`](../compat/laravel-shared.php).

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |            12.6 |      93 |   **73** |              5.4 |     288 |   **99** |
| new app: worker    |             9.7 |     160 |    230 |              4.5 |     389 |    304 |
| new app: prefork   |            12.0 |     106 |    100 |              5.1 |     306 |    180 |
| new app: frankenphp |           13.3 |      85 |    110 |              5.4 |     285 |    201 |
| shared: worker     |            14.7 |      49 |    221 |              5.7 |     244 |    258 |
| shared: prefork    |            14.9 |      48 |    106 |              5.8 |     236 |    137 |
| shared: frankenphp |          **15.7** |    **43** |    101 |            **5.9** |   **235** |    134 |

- **A new application per request gains nothing**: it is at FPM's level at best, with more memory.
- **Shared doubles the API's speed** (93 → 43–48 ms), and prefork matches FrankenPHP on memory.
- **As a full application, the gain is small** (288 → 235 ms): the time goes to Inertia sending the
  whole table on every list, not to the boot, and the build is in `local`, with Inertia's DevTools
  recording each request.
- **The shared mode resets what the application in this benchmark needs**: session, authentication,
  array cache, queued cookies. It is a part of Octane: a real Laravel application should use Octane
  on FrankenPHP, or check [compatibility.md](compatibility.md) first.

## Memory, compared fairly

FPM, worker and prefork carry nginx in their figure, and FrankenPHP does not. `worker_processes auto`
starts 22 processes on this machine, 18.6 MiB; on a 4-core server it would be 6.3 MiB. An estimate,
subtracting the difference from the measured figures, as nginx was measured idle:

| Build                  | fpm | worker | prefork | frankenphp |
|------------------------|----:|-------:|--------:|-----------:|
| Gluo                   |  35 |     96 |      47 |         49 |
| Symfony API            |  48 |    149 |      71 |         68 |
| CakePHP API            |  41 |    147 |      83 |         63 |
| CodeIgniter API        |  43 |    114 |      57 |         60 |
| Laravel API, shared    |  61 |    208 |      93 |        101 |
| Symfony full-stack     |  55 |    196 |     104 |         82 |
| CakePHP full-stack     |  48 |    159 |      94 |         69 |
| CodeIgniter full-stack |  56 |    136 |      77 |         77 |
| Laravel full-stack, shared | 87 |  246 |     125 |        134 |

So FPM is clearly the lowest, and prefork and FrankenPHP are level overall. Prefork wins on Gluo,
CodeIgniter's API and Laravel; FrankenPHP on Symfony and CakePHP. The per-process gap against FPM grows
with more workers, for all of them; the benchmark only ran 4.

## What this benchmark does not show

- **One machine, 4 clients and 4 PHP processes.** It does not show what happens above the workers'
  capacity, or with hundreds of connections.
- **The frameworks are as configured**: CodeIgniter in `development`, Laravel in `local`.
- **The runtime is not a neutral comparison**: FPM and the gateway run on Alpine's NTS PHP 8.5.12,
  FrankenPHP on Debian's ZTS 8.5.11. FrankenPHP's build is faster at bcrypt and
  md5, the other at a plain PHP loop: see [the CPU comparison](benchmark.md#how-each-server-runs).
- **No crash, leak or long-running test.** `-max-requests`, a worker replacing after a fatal error and
  timeouts are covered by the gateway's tests, not measured under load. Nothing here ran for days.
- **Static files were not part of it**: the journey skips the SPA's pages, so nginx's and Caddy's
  speed with them is not measured.
