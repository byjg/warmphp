<?php

// Gluo (byjg/gluo-core 7.0), whose public/app.php is Config::get(Application::class)->handle():
// the worker gets the Application once and hands it every request, as a PSR-7 request since
// php://input is empty in the CLI.

use ByJG\Config\Config;
use ByJG\Gluo\Http\Application;

require getenv('PROJECT_ROOT') . '/vendor/autoload.php';
require_once __DIR__ . '/psr7-request.php';

$app = Config::get(Application::class);

return static fn() => $app->handle(phpworker_psr7_request());
