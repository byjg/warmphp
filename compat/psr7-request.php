<?php

use ByJG\Util\Uri;
use ByJG\WebRequest\Psr7\MemoryStream;
use ByJG\WebRequest\Psr7\ServerRequest;

/**
 * The current request as a PSR-7 request (byjg/webrequest), for an application that takes one,
 * as RestServer's Server::withRequestObject(): php://input is empty in the CLI, so the body
 * comes from the worker.
 */
function warmphp_psr7_request(): ServerRequest
{
    $scheme = ($_SERVER['HTTPS'] ?? '') === 'on' ? 'https' : 'http';
    $request = (new ServerRequest(Uri::getInstance("$scheme://{$_SERVER['HTTP_HOST']}{$_SERVER['REQUEST_URI']}"), $_SERVER, $_COOKIE))
        ->withMethod($_SERVER['REQUEST_METHOD'])
        ->withBody(new MemoryStream(PhpWorker::input()));

    // The other headers are in $_SERVER already; RestServer reads Content-Type from the request
    if (isset($_SERVER['CONTENT_TYPE'])) {
        $request = $request->withHeader('Content-Type', $_SERVER['CONTENT_TYPE']);
    }
    return $request;
}
