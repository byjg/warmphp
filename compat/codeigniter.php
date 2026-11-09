<?php

// CodeIgniter in worker mode, following the worker CodeIgniter 4.7 ships for FrankenPHP
// (vendor/codeigniter4/framework/system/Commands/Worker/Views/frankenphp-worker.php.tpl).

use CodeIgniter\Boot;
use CodeIgniter\Config\Factories;
use CodeIgniter\Config\Services;
use CodeIgniter\Database\Config as DatabaseConfig;
use CodeIgniter\Events\Events;
use Config\Paths;
use Config\WorkerMode;

// PHP_SAPI is "cli", so CodeIgniter's is_cli() would route every request as a CLI command
// (Boot also fixes the app context with it before the first request). Common.php only
// declares is_cli() when it does not exist yet; this worker only serves the web.
function is_cli(): bool
{
    return false;
}

$root = getenv('PROJECT_ROOT');
define('FCPATH', $root . '/public' . DIRECTORY_SEPARATOR);
chdir(FCPATH);

require $root . '/app/Config/Paths.php';
$paths = new Paths();
require $paths->systemDirectory . '/Boot.php';

$app = Boot::bootWorker($paths);
// Kint, which the debug toolbar uses, renders text instead of HTML when PHP_SAPI is "cli";
// the toolbar then cannot json_encode() its data and the page fails
if (class_exists(Kint\Kint::class)) {
    Kint\Kint::$cli_detection = false;
}

// php://input is empty in the CLI, so the request CodeIgniter builds has no body (getJSON() is
// null). pre_system runs right after run() creates the request, before any filter or controller.
Events::on('pre_system', static function (): void {
    service('request')->setBody(PhpWorker::input());
});
/** @var WorkerMode $workerConfig */
$workerConfig = config('WorkerMode');

return static function () use ($app, $workerConfig): void {
    DatabaseConfig::reconnectForWorkerMode();
    Services::reconnectCacheForWorkerMode();
    $app->resetForWorkerMode();

    service('superglobals')
        ->setServerArray($_SERVER)
        ->setGetArray($_GET)
        ->setPostArray($_POST)
        ->setCookieArray($_COOKIE)
        ->setFilesArray($_FILES)
        ->setRequestArray($_REQUEST);

    try {
        $app->run();
    } catch (Throwable $e) {
        Services::exceptions()->exceptionHandler($e);
    }

    // What the FrankenPHP worker does between two requests
    if (Services::has('session')) {
        Services::session()->close();
    }
    DatabaseConfig::cleanupForWorkerMode();
    Factories::reset();
    Services::resetForWorkerMode($workerConfig);
    Events::cleanupForWorkerMode($workerConfig->resetEventListeners);
    if ($workerConfig->forceGarbageCollection) {
        gc_collect_cycles();
    }
    if (CI_DEBUG) {
        Services::toolbar()->reset();
    }
};
