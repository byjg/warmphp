<?php

use Symfony\Component\HttpFoundation\Request;

// php://input is empty in the CLI. Every Request that Symfony's HttpFoundation builds from
// the globals (Laravel's Request::capture() included) gets the body from the worker instead.
Request::setFactory(static fn(
    array $query,
    array $request,
    array $attributes,
    array $cookies,
    array $files,
    array $server,
    $content = null
): Request => new Request($query, $request, $attributes, $cookies, $files, $server, $content ?? PhpWorker::input()));
