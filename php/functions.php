<?php

// The web functions the CLI SAPI drops. The gateway starts PHP with them in
// disable_functions (PHP 8 then lets userland declare them), and these versions
// keep the response in PhpWorker instead.

function header(string $header, bool $replace = true, int $response_code = 0): void
{
    PhpWorker::header($header, $replace, $response_code);
}

function header_remove(?string $name = null): void
{
    PhpWorker::headerRemove($name);
}

function headers_list(): array
{
    return PhpWorker::headersList();
}

function http_response_code(int $response_code = 0): int|bool
{
    return PhpWorker::responseCode($response_code);
}

function setcookie(
    string $name,
    string $value = '',
    array|int $expires_or_options = 0,
    string $path = '',
    string $domain = '',
    bool $secure = false,
    bool $httponly = false
): bool {
    return PhpWorker::cookie(false, $name, $value, $expires_or_options, $path, $domain, $secure, $httponly);
}

function setrawcookie(
    string $name,
    string $value = '',
    array|int $expires_or_options = 0,
    string $path = '',
    string $domain = '',
    bool $secure = false,
    bool $httponly = false
): bool {
    return PhpWorker::cookie(true, $name, $value, $expires_or_options, $path, $domain, $secure, $httponly);
}

function is_uploaded_file(string $filename): bool
{
    return PhpWorker::isUploadedFile($filename);
}

function move_uploaded_file(string $from, string $to): bool
{
    return PhpWorker::moveUploadedFile($from, $to);
}
