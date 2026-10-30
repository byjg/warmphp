<?php

// Laravel in callable mode with ONE application for the worker, what Octane does
// minus its per-request sandbox and state resets.

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

return static function () use ($kernel): void {
    $request = Request::capture();
    $response = $kernel->handle($request);
    $response->send();
    $kernel->terminate($request, $response);
};
