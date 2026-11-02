<?php

// Laravel in callable mode with ONE application for the worker. Laravel's singletons assume one
// request per process, so the state of a request is reset before the next one, with what Octane's
// FlushSessionState, FlushAuthenticationState, FlushArrayCache and FlushQueuedCookies do. Without
// it, the session keeps the previous client's data (its token and its login), its database handler
// updates rows that do not exist, so new sessions are never stored, and the array cache outlives
// the request. Octane does more (a sandbox per request, config, URLs, locale): see
// docs/compatibility.md.

use Illuminate\Contracts\Http\Kernel;
use Illuminate\Http\Request;

$root = getenv('PROJECT_ROOT');
// PHP_SAPI is "cli": without this, $app->runningInConsole() is true (Octane sets it too)
putenv('APP_RUNNING_IN_CONSOLE=false');
define('LARAVEL_START', microtime(true));
require $root . '/vendor/autoload.php';
require __DIR__ . '/request-body.php';

$app = require $root . '/bootstrap/app.php';
$kernel = $app->make(Kernel::class);
// Bootstrapped here, not on the first request: with -prefork, the workers share it
$kernel->bootstrap();

return static function () use ($app, $kernel): void {
    if ($app->resolved('session')) {
        $session = $app['session']->driver();
        $session->flush();
        $session->regenerate();
    }
    if ($app->resolved('auth')) {
        $app->forgetInstance('auth.driver');
        $app['auth']->forgetGuards();
    }
    if ($app->resolved('cache') && config('cache.stores.array')) {
        $app['cache']->store('array')->flush();
    }
    if ($app->resolved('cookie')) {
        $app['cookie']->flushQueuedCookies();
    }

    $request = Request::capture();
    $response = $kernel->handle($request);
    $response->send();
    $kernel->terminate($request, $response);
};
