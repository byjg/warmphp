<?php

use ByJG\WebRequest\Psr7\ServerRequest;

/**
 * The current request as a PSR-7 request (byjg/webrequest), for an application that takes one,
 * as RestServer's Server::withRequestObject(): php://input is empty in the CLI, so the body
 * comes from the worker. The rest (server params, headers, cookies, $_POST, $_FILES) comes from
 * the globals the worker fills.
 */
function warmphp_psr7_request(): ServerRequest
{
    return ServerRequest::fromGlobals(PhpWorker::input());
}
