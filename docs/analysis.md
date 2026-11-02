# Analysis: which server, for which framework

What the [benchmark](benchmark.md) shows: the CRUD journey on 5 frameworks, as APIs and as full
applications, on PHP-FPM, WarmPHP (with and without `-prefork`) and FrankenPHP. All the
numbers are with OPcache on and 4 PHP processes unless it says otherwise.

Sentences marked **(judgment)** are not measured: they come from how each server works, not from
this benchmark.

## The short answer

- **Is WarmPHP valid? With `-prefork`, yes, as a technical result.** It does the framework's
  work within 1.5 ms per request of FrankenPHP with a shared application, ahead of it on CodeIgniter's
  API, and beats FPM on whole journeys in 9 of 11 builds, without touching the application or the PHP build. It is not proven in production.
- **Fastest**: FrankenPHP, in 9 of 11 builds, usually 2–6 % over pre-fork (11 % with Laravel's new
  application per request), and part of that is its PHP build.
- **Least memory**: FPM, in all 11 builds.
- **Without `-prefork`, the gateway has no case**: same speed as pre-fork, with ×1.6 to ×2.1 its memory.
- **What decides most is not the server**: bcrypt is half or more of each journey, and a Laravel
  application with a new instance per request gives away the gain of any worker.

| | fpm | worker | prefork | frankenphp |
|--------------------------------------------|-------:|-------:|-------:|-------:|
| Journeys/s against FPM (median, range)     |      — | ×1.08 (0.80–1.19) | ×1.08 (0.94–1.19) | ×1.13 (1.00–1.26) |
| The 7 steps, faster than FPM (median, range) | — | ×1.27 (0.61–1.92) | ×1.33 (0.89–1.92) | ×1.50 (1.00–2.19) |
| Memory against FPM (median, range)         |      — | ×2.67 (2.09–3.15) | ×1.37 (1.24–1.77) | ×1.17 (1.04–2.04) |
| Failed journeys, of 25,200                 |      0 |      0 |      0 |      0 |
| Application changed                        |     no |     no |     no |     no |
| One boot file per framework needed         |     no |    yes |    yes |    yes |

## Best of

| Category                                    | Winner                                   | Numbers                                                                        |
|---------------------------------------------|------------------------------------------|--------------------------------------------------------------------------------|
| Most journeys/s                             | **CakePHP API on FrankenPHP**            | 18.2; Gluo 17.9; CakePHP's prefork 17.4, FPM 16.1                              |
| Most journeys/s, FPM                        | **CakePHP API**                          | 16.1; Gluo 15.4, CodeIgniter API 14.7                                          |
| Best full application                       | **CakePHP**                              | 15.2 on FrankenPHP, 14.3 on prefork, 13.0 on FPM                               |
| Fastest framework code (7 steps)            | **Gluo**                                 | 26 ms; Symfony and CakePHP APIs 32–37; with FPM, Gluo 49, CakePHP 54           |
| Pages of 50 products                        | **Gluo**                                 | 2.0–2.8 ms; CakePHP and Symfony 2.7–4.1                                        |
| Read one product                            | **Gluo**                                 | 0.8–1.1 ms; 1.8–2.2 for Symfony and CakePHP                                    |
| Writes (edit + add)                         | **Gluo**                                 | 16 ms; Symfony and CakePHP 19; Laravel 21; CodeIgniter 30                      |
| Least memory                                | **Gluo on FPM**                          | 48 MiB; 50 on FrankenPHP, 60 on prefork                                        |
| Least damage without OPcache                | **Gluo**, among the FPM runs             | 9.5 journeys/s, 186 ms; the other APIs 3.8–7.3, 310–710 ms                     |
| Largest gain from staying booted            | **Symfony API**                          | 69 → 32 ms on FrankenPHP (×2.2), 36 on prefork; Laravel API shared 94 → 44     |
| Smallest gain                               | **CodeIgniter**                          | 75 → 64 ms as an API; most of its work happens on every request                |
| Slowest                                     | **Laravel full-stack**                   | 5.8 journeys/s at best: it has no paging, so each list sends the whole table through Inertia |

Gluo and CakePHP are tied at the top: what separates their journeys/s is mostly the login (196 ms
against 184 on FrankenPHP), and Gluo's 7 steps are faster. Symfony comes last among the APIs in
journeys/s only because its hasher uses cost 13, twice the work: its 7 steps are in the top group.

## Each server

### PHP-FPM

**Strongest at: memory, and asking nothing.** One OPcache shared by all its children, and the
application freed after every request.

- **Measured, strong**: the least memory in all 11 builds, 48–101 MiB. With nginx at 4 processes, not
  this machine's 22, that is about 36–89 MiB. Steady on APIs: p99 is 1.09–1.16 × p50.
- **Measured, weak**: the slowest for real work in 9 of 11 builds. Each request pays for the boot:
  Gluo's 7 steps take 49 ms against 26, Symfony's API 69 against 32. Without OPcache it falls
  apart, ×1.6 to ×3.3 fewer journeys/s, and it is the only server that does.
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

- **Measured, strong**: the most journeys/s in 9 of 11 builds (worker is 0.1 ahead on full-stack CodeIgniter,
  and FPM ties it on full-stack Laravel, new app), the best 7 steps in 9 of 11 (not CodeIgniter's
  two builds), and nearly flat with OPcache off. Memory close to FPM's on the simple builds: Gluo 50
  against 48 MiB, with nginx not
  included, so it serves static files in that figure too.
- **Measured, weak**: Laravel with a new application per request costs twice FPM's memory (206 against
  101 MiB, full-stack), the largest ratio of any build. Part of its lead is not the server: its PHP
  build takes 5.6 % less time on bcrypt than the Alpine build the others use, and on Gluo the login
  is most of what separates it from prefork. Without nginx, the memory is not as far from
  prefork as it looks: with nginx at 4 processes, prefork uses less on Gluo (47 against 50),
  CodeIgniter's API (58 against 59) and Laravel's (93 against 99).
- **Pros (judgment)**: an official Laravel integration through Octane, HTTP/2, HTTP/3 and HTTPS built
  in, one binary to ship, and an active project.
- **Cons (judgment)**: it requires a ZTS PHP, so its own image or build, and C extensions must be
  thread-safe. It replaces nginx: a team with an nginx setup changes how it runs. PHP runs inside the
  server process, so a crash in an extension takes everything down. The application must be free of
  leaks across requests, as in any worker.

**Use it** for a new deployment, or one that can change its web server and image. **Avoid it** when
you depend on an extension that is not thread-safe, or your PHP image and nginx are fixed.

### WarmPHP with `-prefork`

**Strongest at: a worker's speed with what you already run.** The plain PHP CLI, NTS, from any image,
behind any proxy, close to FrankenPHP in speed and memory.

- **Measured, strong**: ×1.08 FPM's journeys/s and ×1.33 on the 7 steps at the median, up to ×1.92.
  The 7 steps are ahead of FrankenPHP on CodeIgniter's API (64 against 66) and level on the full-stack
  build (151 against 153); 2 ms behind on Gluo, 3–4 ms on the Symfony and CakePHP APIs, for 7
  requests, so about 0.5 ms per request, and 9 ms behind on full-stack CakePHP, its largest gap with a
  shared application. It uses ×1.6 to ×2.1 less memory than the plain worker (Gluo 108 → 60 MiB,
  Laravel API 221 → 106), with no loss of speed. With OPcache off, its memory stays near FPM's on some builds and it is
  as fast as with OPcache on.
- **Measured, weak**: behind FrankenPHP in journeys/s, except on full-stack CodeIgniter: 2–6 %, and 11 %
  with the Laravel API's new application per request.
  More memory than FPM, ×1.24 to ×1.77, because the booted application's memory gets unshared as
  PHP's reference counts touch it. Symfony and CakePHP lose the most (CakePHP's API: 95 against 55 MiB).
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

### WarmPHP without `-prefork`

**Strongest at: simplicity of the process model.** Each worker is an independent `php` that boots by
itself.

- **Measured, strong**: the same speed as prefork in the shared-application builds, within the noise.
  With OPcache off it barely loses speed.
- **Measured, weak**: the most memory in all 11 builds, ×2.09 to ×3.15 FPM's, since each worker
  keeps its own OPcache and application. The worst with Laravel's new application per request: 9.9
  journeys/s against 11.9 on prefork, and p99 at 2.08 × p50, as every `-max-requests` is a cold boot.
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
| fpm        |       15.4 |      49 | **48** |
| worker     |       16.8 |      28 |    108 |
| prefork    |       16.9 |      28 |     60 |
| frankenphp |   **17.9** |  **26** |     50 |

The fastest and smallest framework here on any server. FPM's gap is only the boot, about 20 ms in 7 steps.
Prefork is 2 ms behind FrankenPHP in those 7 steps: most of the difference in journeys/s is the login.

### Symfony

| Server     | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm        |             8.6 |      69 | **61** |              7.6 |     117 | **66** |
| worker     |             9.4 |      37 |    162 |              8.4 |      72 |    207 |
| prefork    |             9.3 |      36 |     83 |              8.5 |      68 |    116 |
| frankenphp |         **9.8** |  **32** |     67 |          **8.9** |  **63** |     83 |

The clearest gain from staying booted: about half the time on any worker. Its low journeys/s come from
bcrypt at cost 13, which hides this. Worst on FPM without OPcache after Laravel: 503 ms in 7 steps, as an API.

### CakePHP

| Server     | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm        |            16.1 |      54 | **55** |             13.0 |     106 | **62** |
| worker     |            17.3 |      37 |    159 |             14.1 |      83 |    171 |
| prefork    |            17.4 |      35 |     95 |             14.3 |      80 |    107 |
| frankenphp |        **18.2** |  **32** |     59 |         **15.2** |  **70** |     68 |

The best journeys/s, and the best full application. Here FrankenPHP is the clear pick over prefork:
faster, and 30–40 MiB less. This is prefork's worst memory result among the shared-application builds.

### CodeIgniter

| Server     | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm        |            14.7 |      75 | **54** |              9.4 |     185 | **71** |
| worker     |            15.1 |      65 |    125 |         **10.4** | **147** |    148 |
| prefork    |            15.3 |  **64** |     70 |             10.3 |     151 |     89 |
| frankenphp |        **15.6** |      66 |     59 |             10.3 |     153 |     82 |

The smallest gain, and all the workers are tied. Most of its work happens on every request, and it
runs in `development`, as configured, with the debug toolbar writing a file per request. FPM with
OPcache is a sound choice here, unless the application is on `production` and measures otherwise.

### Laravel

Three ways to run it. FPM is the reference, "new app" builds an application per request, and "shared"
keeps one, with the resets of [`compat/laravel-shared.php`](../compat/laravel-shared.php).

| Server             | API: journeys/s | 7 steps | memory | Full: journeys/s | 7 steps | memory |
|--------------------|----------------:|--------:|-------:|-----------------:|--------:|-------:|
| fpm                |            12.4 |      94 | **73** |              5.3 |     291 | **101** |
| new app: worker    |             9.9 |     154 |    176 |              4.4 |     390 |    302 |
| new app: prefork   |            11.9 |     105 |    101 |              5.0 |     318 |    152 |
| new app: frankenphp |            13.2 |      86 |    107 |              5.3 |     290 |    206 |
| shared: worker     |            14.8 |      49 |    221 |              5.7 |     247 |    259 |
| shared: prefork    |            14.7 |      49 |    106 |              5.7 |     245 |    137 |
| shared: frankenphp |        **15.6** |  **44** |     99 |          **5.8** | **239** |    131 |

- **A new application per request gains nothing**: it is at FPM's level at best, with more memory.
- **Shared doubles the API's speed** (94 → 44–49 ms), and prefork is close to FrankenPHP on memory.
- **As a full application, the gain is small** (291 → 239 ms): the time goes to Inertia sending the
  whole table on every list, not to the boot, and the build is in `local`, with Inertia's DevTools
  recording each request.
- **The shared mode resets what the application in this benchmark needs**: session, authentication,
  array cache, queued cookies. It is a part of Octane: a real Laravel application should use Octane
  on FrankenPHP, or check [compatibility.md](compatibility.md) first.

## OPcache on a worker

Once booted, a worker has already compiled its code, so OPcache changes little: the same speed on
APIs, and up to 11 % on full-stack applications. The exception is rebuilding the application per
request, as `compat/laravel-fresh.php` does, where it costs about 20 to 45 % on prefork and
FrankenPHP. FPM, which boots on every request, takes 3 to 8 times longer without it.

| Group                                | The 7 steps, OPcache on → off                                                                  |
|--------------------------------------|------------------------------------------------------------------------------------------------|
| APIs and Gluo, shared application    | Mostly the same; the worst is Laravel API, shared, at +4 to +8 %                               |
| Full-stack, shared application       | From no change (Symfony on worker, 72 → 72 ms) to +11 % (CodeIgniter on FrankenPHP, 153 → 170) |
| Laravel, new application per request | API: prefork 105 → 145 ms, FrankenPHP 86 → 125; full-stack: 318 → 390, 290 → 356               |

These numbers exclude the boot and the replacement of a worker: the first requests compile the code,
and a plain worker compiles it again each time it is replaced. Keep OPcache on in production
**(judgment)**.

## Memory, compared fairly

FPM, worker and prefork carry nginx in their figure, and FrankenPHP does not. `worker_processes auto`
starts 22 processes on this machine, 18.6 MiB; on a 4-core server it would be 6.3 MiB. An estimate,
subtracting the difference from the measured figures, as nginx was measured idle:

| Build                  | fpm | worker | prefork | frankenphp |
|------------------------|----:|-------:|--------:|-----------:|
| Gluo                   |  36 |     96 |      47 |         50 |
| Symfony API            |  48 |    149 |      71 |         67 |
| CakePHP API            |  42 |    147 |      82 |         59 |
| CodeIgniter API        |  42 |    113 |      58 |         59 |
| Laravel API, shared    |  61 |    209 |      93 |         99 |
| Symfony full-stack     |  53 |    195 |     104 |         83 |
| CakePHP full-stack     |  49 |    159 |      95 |         68 |
| CodeIgniter full-stack |  58 |    136 |      77 |         82 |
| Laravel full-stack, shared |  89 |    246 |     125 |        131 |

So FPM is clearly the lowest, and prefork and FrankenPHP are level overall. Prefork wins on Gluo,
CodeIgniter and Laravel; FrankenPHP on Symfony and CakePHP. The per-process gap against FPM grows
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
