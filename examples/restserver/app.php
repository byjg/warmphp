<?php

use ByJG\RestServer\ErrorHandler;
use ByJG\RestServer\HttpRequest;
use ByJG\RestServer\HttpResponse;
use ByJG\RestServer\Route\Route;
use ByJG\RestServer\Route\RouteList;
use ByJG\RestServer\Server;

// Boot once: autoload, routes (or an OpenAPI route list) and anything else expensive
require __DIR__ . '/vendor/autoload.php';
require_once __DIR__ . '/../../compat/psr7-request.php';

$bootedAt = date('c');
$requests = 0;

$routes = (new RouteList())
    ->addRoute(Route::get('/hello')->withClosure(function (HttpResponse $response, HttpRequest $request) use ($bootedAt, &$requests) {
        $response->write([
            'hello' => $request->query('name', 'world'),
            'pid' => getmypid(),
            'booted_at' => $bootedAt,
            'requests_served' => ++$requests,
        ]);
    }))
    ->addRoute(Route::post('/echo')->withClosure(function (HttpResponse $response, HttpRequest $request) {
        $response->setResponseCode(201);
        $response->addHeader('X-Echo', 'yes');
        $response->addCookie('echoed', 'yes');
        $response->write(['received' => json_decode($request->payload(), true)]);
    }));

// Every request: php://input is empty in the CLI, so the Server gets a PSR-7 request with
// the worker's globals and body (Server::withRequestObject(), byjg/restserver 7.0)
return static function () use ($routes): void {
    try {
        (new Server())->withRequestObject(warmphp_psr7_request())->handle($routes);
    } catch (Throwable $exception) {
        // In php-fpm the exception handler RestServer registers renders it (404, 401, 422...)
        ErrorHandler::getInstance()->handleException($exception);
    }
};
