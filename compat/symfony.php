<?php

// Symfony in callable mode, following symfony/runtime's FrankenPhpWorkerRunner:
// one kernel for the worker; Kernel::handle() resets the services tagged kernel.reset
// before every request after the first.

use App\Kernel;
use Symfony\Component\Dotenv\Dotenv;
use Symfony\Component\HttpFoundation\Request;

$root = getenv('PROJECT_ROOT');
require $root . '/vendor/autoload.php';
require __DIR__ . '/request-body.php';

// PHP_SAPI is "cli": tell Symfony it serves the web from a worker, as symfony/runtime does
// for FrankenPHP (otherwise error pages use the CLI renderer)
putenv('APP_RUNTIME_MODE=web=1&worker=1');
$_ENV['APP_RUNTIME_MODE'] = $_SERVER['APP_RUNTIME_MODE'] = 'web=1&worker=1';

// What public/index.php gets from vendor/autoload_runtime.php
(new Dotenv())->bootEnv($root . '/.env');
$kernel = new Kernel($_SERVER['APP_ENV'], (bool)$_SERVER['APP_DEBUG']);
// Booted here, not on the first request: with -prefork, the workers share it
$kernel->boot();

return static function () use ($kernel): void {
    $request = Request::createFromGlobals();
    $response = $kernel->handle($request);
    $response->send();
    $kernel->terminate($request, $response);
};
