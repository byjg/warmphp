<?php

// FrankenPHP's worker mode with the gateway's boot files: PHPWORKER_BOOT (a file of compat/) runs
// once per worker thread, then frankenphp_handle_request() hands its handler every request.
// FrankenPHP is a web SAPI, so php://input works: the boot files get the PhpWorker::input() they
// call from this shim. PHPWORKER_MAX_REQUESTS ends the script after that many requests, and
// FrankenPHP starts it again, as the gateway's -max-requests replaces a worker.

final class PhpWorker
{
    public static function input(): string
    {
        return (string)file_get_contents('php://input');
    }
}

$handler = require __DIR__ . '/' . basename((string)getenv('PHPWORKER_BOOT'));
$maxRequests = (int)getenv('PHPWORKER_MAX_REQUESTS');

for ($requests = 1; frankenphp_handle_request($handler); $requests++) {
    // As FrankenPHP's documentation does after each request
    gc_collect_cycles();
    if ($maxRequests > 0 && $requests >= $maxRequests) {
        break;
    }
}
