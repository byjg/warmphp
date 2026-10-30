<?php

/**
 * Persistent PHP worker for phpworker-gateway. Plain PHP CLI: no SAPI, no FPM, no extension.
 *
 * Usage: php -d disable_functions=<list> worker.php <callable|script> <entry.php>
 *        php worker.php disable-functions   (prints <list>)
 *
 *   callable  entry.php runs ONCE and returns the request handler (a callable).
 *             Everything it builds (container, routes, connections) stays in memory.
 *   script    entry.php is included again on every request (front-controller style).
 *             Loaded classes and static caches stay in memory, objects do not.
 *
 * The gateway sends one request at a time on STDIN: a 4-byte big-endian length followed by
 * a JSON document. During the request, STDOUT is the response body, as in a web SAPI. The
 * request's boundary ends the body, followed by the status and headers, framed the same way.
 */

declare(strict_types=1);

final class PhpWorker
{
    /** Web functions the CLI SAPI drops; functions.php redefines them. */
    public const OVERRIDDEN = [
        'header', 'header_remove', 'headers_list', 'http_response_code',
        'setcookie', 'setrawcookie', 'is_uploaded_file', 'move_uploaded_file',
    ];

    private const COOKIE_INVALID_CHARS = ",; \t\r\n\013\014";

    /** @var list<string> */
    private static array $headers = [];
    private static int $status = 200;
    private static string $input = '';
    private static string $entry = '';
    private static bool $inRequest = false;
    /** Written after the body; the gateway sends a new random one with every request */
    private static string $boundary = '';
    /** Whether the buffer that writes the body is still open (the application can close it) */
    private static bool $capturing = false;
    /** @var array<string, true> temporary files of this request's uploads */
    private static array $uploads = [];
    /** Session id the client has: the one in its cookie, or the last session cookie sent */
    private static string $sessionCookieId = '';

    /**
     * Raw request body. Replacement for php://input, which is always empty in the CLI.
     */
    public static function input(): string
    {
        return self::$input;
    }

    public static function header(string $header, bool $replace = true, int $responseCode = 0): void
    {
        if (strpbrk($header, "\r\n\0") !== false) {
            trigger_error('Header may not contain more than a single header, new line detected', E_USER_WARNING);
            return;
        }
        self::sendSessionCookie();
        if (preg_match('~^HTTP/\S+\s+(\d{3})~i', $header, $match) === 1) {
            self::$status = (int)$match[1];
            return;
        }

        [$name, $value] = array_pad(explode(':', $header, 2), 2, null);
        if ($value === null) {
            return;
        }
        $name = trim($name);
        if ($responseCode > 0) {
            self::$status = $responseCode;
        }
        if (strcasecmp($name, 'Status') === 0) {
            self::$status = (int)trim($value);
            return;
        }

        if ($replace) {
            self::headerRemove($name);
        }
        self::$headers[] = rtrim($header);

        // A Location without a redirect status becomes a 302
        if ($responseCode === 0 && strcasecmp($name, 'Location') === 0
            && self::$status !== 201 && (self::$status < 300 || self::$status > 399)) {
            self::$status = 302;
        }
    }

    public static function headerRemove(?string $name): void
    {
        self::$headers = $name === null ? [] : array_values(array_filter(
            self::$headers,
            static fn(string $header): bool => strcasecmp(trim(explode(':', $header, 2)[0]), trim($name)) !== 0
        ));
    }

    /** @return list<string> */
    public static function headersList(): array
    {
        return self::$headers;
    }

    public static function responseCode(int $code): int
    {
        $previous = self::$status;
        if ($code > 0) {
            self::$status = $code;
        }
        return $previous;
    }

    /**
     * setcookie() and setrawcookie(), producing the same Set-Cookie line as PHP.
     */
    public static function cookie(
        bool $raw,
        string $name,
        string $value,
        array|int $expiresOrOptions,
        string $path,
        string $domain,
        bool $secure,
        bool $httponly
    ): bool {
        $function = $raw ? 'setrawcookie' : 'setcookie';
        $options = [
            'expires' => 0, 'path' => $path, 'domain' => $domain, 'secure' => $secure,
            'httponly' => $httponly, 'samesite' => '', 'partitioned' => false,
        ];
        if (is_array($expiresOrOptions)) {
            foreach ($expiresOrOptions as $key => $option) {
                $key = strtolower((string)$key);
                if (!array_key_exists($key, $options)) {
                    throw new ValueError("$function(): option \"$key\" is invalid");
                }
                $options[$key] = $option;
            }
        } else {
            $options['expires'] = $expiresOrOptions;
        }
        foreach (['path', 'domain', 'samesite'] as $option) {
            $options[$option] = (string)$options[$option];
        }

        if ($name === '') {
            throw new ValueError("$function(): Argument #1 (\$name) cannot be empty");
        }
        if (strpbrk($name, '=' . self::COOKIE_INVALID_CHARS) !== false) {
            throw new ValueError("$function(): Argument #1 (\$name) cannot contain \"=\", \",\", \";\", \" \", \"\\t\", \"\\r\", \"\\n\", \"\\013\", or \"\\014\"");
        }
        if ($raw && strpbrk($value, self::COOKIE_INVALID_CHARS) !== false) {
            throw new ValueError("$function(): Argument #2 (\$value) cannot contain \",\", \";\", \" \", \"\\t\", \"\\r\", \"\\n\", \"\\013\", or \"\\014\"");
        }
        foreach (['path', 'domain'] as $option) {
            if (strpbrk($options[$option], self::COOKIE_INVALID_CHARS) !== false) {
                throw new ValueError("$function(): \"$option\" option cannot contain \",\", \";\", \" \", \"\\t\", \"\\r\", \"\\n\", \"\\013\", or \"\\014\"");
            }
        }

        $cookie = "$name=";
        if ($value === '') {
            $cookie .= 'deleted; expires=Thu, 01 Jan 1970 00:00:01 GMT; Max-Age=0';
        } else {
            $cookie .= $raw ? $value : rawurlencode($value);
            $expires = (int)$options['expires'];
            if ($expires > 0) {
                $cookie .= '; expires=' . gmdate('D, d M Y H:i:s', $expires) . ' GMT; Max-Age=' . max(0, $expires - time());
            }
        }
        $cookie .= ($options['path'] !== '' ? "; path={$options['path']}" : '')
            . ($options['domain'] !== '' ? "; domain={$options['domain']}" : '')
            . ($options['secure'] ? '; secure' : '')
            . ($options['httponly'] ? '; HttpOnly' : '')
            . ($options['samesite'] !== '' ? "; SameSite={$options['samesite']}" : '')
            . ($options['partitioned'] ? '; Partitioned' : '');

        self::header("Set-Cookie: $cookie", false);
        return true;
    }

    public static function isUploadedFile(string $filename): bool
    {
        return isset(self::$uploads[$filename]);
    }

    public static function moveUploadedFile(string $from, string $to): bool
    {
        if (!self::isUploadedFile($from) || !rename($from, $to)) {
            return false;
        }
        unset(self::$uploads[$from]);
        chmod($to, 0666 & ~umask());
        return true;
    }

    public static function run(string $mode, string $entry): void
    {
        self::$entry = $entry;
        ini_set('display_errors', 'stderr');
        self::captureStrayOutput();
        chdir(dirname($entry));

        $handler = $mode === 'script'
            ? static function () use ($entry): void {
                require $entry;
            }
            : require $entry;

        if (!is_callable($handler)) {
            fwrite(STDERR, "$entry must return a callable when the worker runs in callable mode\n");
            exit(1);
        }

        // exit() and fatal errors end the process in the middle of a request: still answer it
        register_shutdown_function(static function (): void {
            if (!self::$inRequest) {
                return;
            }
            $error = error_get_last();
            if ($error !== null && in_array($error['type'], [E_ERROR, E_PARSE, E_CORE_ERROR, E_COMPILE_ERROR, E_USER_ERROR], true)) {
                self::$status = 500;
            }
            self::finish(true);
        });

        self::write(['ready' => true, 'pid' => getmypid()]);

        while (($request = self::read()) !== null) {
            self::begin($request);
            try {
                $handler();
            } catch (Throwable $exception) {
                fwrite(STDERR, "PHP Fatal error:  Uncaught $exception\n");
                self::$status = 500;
            }
            self::finish(false);
        }
    }

    /**
     * Output outside a request (boot messages, stray echoes) goes to STDERR,
     * so it never mixes with a response body on STDOUT.
     */
    private static function captureStrayOutput(): void
    {
        ob_start(static function (string $buffer): string {
            if ($buffer !== '') {
                fwrite(STDERR, $buffer);
            }
            return '';
        }, 1);
    }

    private static function begin(array $request): void
    {
        // Boot or stray output still buffered goes to STDERR; from here STDOUT is the body
        while (ob_get_level() > 0 && @ob_end_flush()) {
        }
        self::$boundary = (string)$request['boundary'];

        // Environment variables stay visible in $_SERVER, as in the CLI and FrankenPHP
        $_SERVER = $request['server'] + [
            'SCRIPT_FILENAME' => self::$entry,
            'SCRIPT_NAME' => '/' . basename(self::$entry),
            'PHP_SELF' => '/' . basename(self::$entry),
            'DOCUMENT_ROOT' => dirname(self::$entry),
            'REQUEST_TIME' => time(),
            'REQUEST_TIME_FLOAT' => microtime(true),
        ] + getenv();
        self::$input = base64_decode((string)($request['body'] ?? ''));

        $_GET = self::parse($_SERVER['QUERY_STRING'] ?? '');
        $_POST = [];
        $_FILES = [];
        self::$uploads = [];
        if (($_SERVER['REQUEST_METHOD'] ?? '') === 'POST') {
            $type = strtolower($_SERVER['CONTENT_TYPE'] ?? '');
            if (str_starts_with($type, 'application/x-www-form-urlencoded')) {
                $_POST = self::parse(self::$input);
            } elseif (str_starts_with($type, 'multipart/form-data')) {
                $_POST = self::fields($request['fields'] ?? []);
                $_FILES = self::files($request['files'] ?? []);
                foreach ($request['files'] ?? [] as $upload) {
                    if ($upload['tmp_name'] !== '') {
                        self::$uploads[$upload['tmp_name']] = true;
                    }
                }
            }
        }
        $_COOKIE = self::cookies($_SERVER['HTTP_COOKIE'] ?? '');
        $_REQUEST = array_replace($_GET, $_POST);
        self::beginSession();

        self::$headers = [];
        self::$status = 200;
        error_clear_last();
        self::$inRequest = true;
        self::captureBody();
    }

    /**
     * The body goes to STDOUT from this buffer's callback, never through PHP's output layer: in
     * the CLI the first byte that reaches it marks the headers as sent for the rest of the
     * process, and session_start(), session_id() and the session.* settings stop working.
     * The application may close this buffer (CodeIgniter's Events.php closes them all on every
     * request): PHP still passes the content to the callback, which writes it in order.
     */
    private static function captureBody(): void
    {
        self::$capturing = true;
        ob_start(static function (string $buffer, int $phase): string {
            if (($phase & PHP_OUTPUT_HANDLER_CLEAN) === 0) {
                fwrite(STDOUT, $buffer);
            }
            if (($phase & PHP_OUTPUT_HANDLER_FINAL) !== 0) {
                self::$capturing = false;
            }
            return '';
        }, 65536);
    }

    private static function finish(bool $exiting): void
    {
        // Buffers the application left open belong to the body. They are flushed into the one
        // below; if the application closed ours, the last one is collected without its callback,
        // so nothing reaches PHP's output layer.
        while (ob_get_level() > 1 && @ob_end_flush()) {
        }
        if (ob_get_level() === 1) {
            if (self::$capturing) {
                ob_end_flush();
            } else {
                fwrite(STDOUT, (string)ob_get_clean());
            }
        }
        self::$inRequest = false;
        self::finishSession();

        // Like the web SAPI, send default_mimetype when the application set no Content-Type
        $mimeType = (string)ini_get('default_mimetype');
        if ($mimeType !== '' && !self::hasHeader('Content-Type')) {
            $charset = (string)ini_get('default_charset');
            self::$headers[] = "Content-type: $mimeType" . ($charset !== '' && str_starts_with($mimeType, 'text/') ? "; charset=$charset" : '');
        }

        self::write([
            'status' => self::$status,
            'headers' => self::$headers,
            // Output that reached PHP's output layer anyway (every buffer closed, then echo) is in
            // the body, but it left the process with "headers sent": the gateway replaces it
            'exit' => $exiting || headers_sent(),
        ], self::$boundary);
        self::captureStrayOutput();
    }

    /**
     * session_start() reads the id from the cookies PHP parsed at startup, never from the
     * $_COOKIE set here, so the id of this request's cookie is set before the application starts.
     */
    private static function beginSession(): void
    {
        unset($_SESSION);
        if (session_status() === PHP_SESSION_DISABLED) {
            return;
        }
        $id = $_COOKIE[session_name()] ?? '';
        self::$sessionCookieId = is_string($id) && preg_match('/^[a-zA-Z0-9,-]{1,256}$/', $id) === 1 ? $id : '';
        session_id(self::$sessionCookieId);
    }

    /**
     * In the web SAPI, session_start() and session_regenerate_id() send the session cookie at
     * once, before any header the application sets afterwards (Symfony replaces or deletes it,
     * CakePHP deletes the old one before regenerating). Called before every header and at the
     * end of the request, this sends the cookie of a new id at that same position.
     */
    private static function sendSessionCookie(): void
    {
        if (session_status() === PHP_SESSION_DISABLED || !ini_get('session.use_cookies')) {
            return;
        }
        $id = session_id();
        if ($id === '' || $id === self::$sessionCookieId) {
            return;
        }
        self::$sessionCookieId = $id;

        $params = session_get_cookie_params();
        self::cookie(true, session_name(), $id, [
            'expires' => $params['lifetime'] > 0 ? time() + $params['lifetime'] : 0,
            'path' => $params['path'],
            'domain' => $params['domain'],
            'secure' => $params['secure'],
            'httponly' => $params['httponly'],
            'samesite' => $params['samesite'],
            'partitioned' => $params['partitioned'] ?? false,
        ], '', '', false, false);
    }

    /**
     * Saves the session and sends the rest of what session_start() sends in the web SAPI:
     * the cookie of a new or regenerated id, if no header came after it, and the cache limiter headers.
     */
    private static function finishSession(): void
    {
        if (session_status() === PHP_SESSION_DISABLED) {
            return;
        }
        $started = isset($_SESSION);
        if (session_status() === PHP_SESSION_ACTIVE) {
            session_write_close();
        }

        self::sendSessionCookie();

        if ($started) {
            // Headers the application set itself take precedence (Last-Modified is not sent)
            $maxAge = (int)session_cache_expire() * 60;
            $cacheHeaders = match (session_cache_limiter()) {
                'nocache' => ['Expires' => 'Thu, 19 Nov 1981 08:52:00 GMT', 'Cache-Control' => 'no-store, no-cache, must-revalidate', 'Pragma' => 'no-cache'],
                'public' => ['Expires' => gmdate('D, d M Y H:i:s', time() + $maxAge) . ' GMT', 'Cache-Control' => "public, max-age=$maxAge"],
                'private' => ['Expires' => 'Thu, 19 Nov 1981 08:52:00 GMT', 'Cache-Control' => "private, max-age=$maxAge"],
                'private_no_expire' => ['Cache-Control' => "private, max-age=$maxAge"],
                default => [],
            };
            foreach ($cacheHeaders as $name => $value) {
                if (!self::hasHeader($name)) {
                    self::$headers[] = "$name: $value";
                }
            }
        }
    }

    private static function hasHeader(string $name): bool
    {
        foreach (self::$headers as $header) {
            if (strcasecmp(trim(explode(':', $header, 2)[0]), $name) === 0) {
                return true;
            }
        }
        return false;
    }

    private static function parse(string $query): array
    {
        parse_str($query, $result);
        return $result;
    }

    /**
     * Builds a query string so parse_str() applies PHP's own rules for names like a[b] and a[].
     *
     * @param list<array{0: string, 1: string}> $pairs
     */
    private static function fields(array $pairs): array
    {
        $query = [];
        foreach ($pairs as [$name, $value]) {
            $query[] = str_replace(['%5B', '%5D'], ['[', ']'], rawurlencode($name)) . '=' . rawurlencode((string)$value);
        }
        return self::parse(implode('&', $query));
    }

    /**
     * Builds $_FILES with PHP's layout: $_FILES[field][attribute][...nested keys].
     *
     * @param list<array{field: string, name: string, type: string, tmp_name: string, error: int, size: int}> $uploads
     */
    private static function files(array $uploads): array
    {
        $files = [];
        foreach (['name', 'full_path', 'type', 'tmp_name', 'error', 'size'] as $attribute) {
            $source = $attribute === 'full_path' ? 'name' : $attribute;
            $pairs = array_map(static fn(array $upload): array => [$upload['field'], (string)$upload[$source]], $uploads);
            foreach (self::fields($pairs) as $field => $value) {
                if ($attribute === 'error' || $attribute === 'size') {
                    if (is_array($value)) {
                        array_walk_recursive($value, static function (mixed &$item): void {
                            $item = (int)$item;
                        });
                    } else {
                        $value = (int)$value;
                    }
                }
                $files[$field][$attribute] = $value;
            }
        }
        return $files;
    }

    private static function cookies(string $header): array
    {
        $cookies = [];
        foreach (explode(';', $header) as $pair) {
            [$name, $value] = array_pad(explode('=', $pair, 2), 2, '');
            $name = trim($name);
            if ($name !== '' && !isset($cookies[$name])) {
                $cookies[$name] = urldecode(trim($value));
            }
        }
        return $cookies;
    }

    private static function read(): ?array
    {
        $size = self::readBytes(4);
        if ($size === null) {
            return null;
        }
        $json = self::readBytes(unpack('N', $size)[1]);
        return $json === null ? null : json_decode($json, true, 512, JSON_THROW_ON_ERROR);
    }

    private static function readBytes(int $length): ?string
    {
        $data = '';
        while (strlen($data) < $length) {
            $chunk = fread(STDIN, $length - strlen($data));
            if ($chunk === false || ($chunk === '' && feof(STDIN))) {
                return null;
            }
            $data .= $chunk;
        }
        return $data;
    }

    private static function write(array $message, string $prefix = ''): void
    {
        $json = json_encode($message, JSON_THROW_ON_ERROR | JSON_INVALID_UTF8_SUBSTITUTE | JSON_UNESCAPED_SLASHES);
        $data = $prefix . pack('N', strlen($json)) . $json;
        for ($written = 0; $written < strlen($data); $written += $count) {
            $count = fwrite(STDOUT, substr($data, $written));
            if ($count === false || $count === 0) {
                exit(1); // The gateway is gone
            }
        }
        fflush(STDOUT);
    }
}

// The gateway asks for the disable_functions value to start workers with: php.ini's own list plus ours
if ($argc === 2 && $argv[1] === 'disable-functions') {
    $current = array_filter(array_map('trim', explode(',', (string)ini_get('disable_functions'))));
    echo implode(',', array_unique([...$current, ...PhpWorker::OVERRIDDEN]));
    exit(0);
}

if ($argc !== 3 || !in_array($argv[1], ['callable', 'script'], true) || ($entry = realpath($argv[2])) === false) {
    fwrite(STDERR, "usage: php -d disable_functions=<list> worker.php <callable|script> <entry.php>\n");
    exit(2);
}

$enabled = array_values(array_filter(PhpWorker::OVERRIDDEN, 'function_exists'));
if ($enabled !== []) {
    fwrite(STDERR, 'worker.php needs these functions in disable_functions: ' . implode(',', $enabled) . "\n");
    exit(1);
}
require __DIR__ . '/functions.php';

PhpWorker::run($argv[1], $entry);
