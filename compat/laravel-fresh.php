<?php

// Laravel in worker mode with a NEW application per request: public/index.php
// without re-including it. Only the autoloader and loaded classes stay in memory.

use Illuminate\Http\Request;

$root = getenv('PROJECT_ROOT');
// PHP_SAPI is "cli": without this, $app->runningInConsole() is true (Octane sets it too)
putenv('APP_RUNNING_IN_CONSOLE=false');
define('LARAVEL_START', microtime(true));
require $root . '/vendor/autoload.php';
require __DIR__ . '/request-body.php';

return static function () use ($root): void {
    $app = require $root . '/bootstrap/app.php';
    $app->handleRequest(Request::capture());
};
