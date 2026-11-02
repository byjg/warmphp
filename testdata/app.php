<?php

// Boot: runs once per worker process (with -prefork, once in the master)
echo "boot output must go to stderr, not to the protocol stream\n";
require __DIR__ . '/Namespaced.php';
session_save_path(sys_get_temp_dir());
$boot = bin2hex(random_bytes(4));
mt_rand(); // seeds mt_rand() at boot: with -prefork, every child must still get its own sequence
$count = 0;

return static function () use ($boot, &$count): void {
    $count++;
    switch (parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH)) {
        case '/count':
            echo json_encode(['boot' => $boot, 'count' => $count, 'pid' => getmypid(), 'rand' => mt_rand()]);
            break;

        case '/vars':
            $contents = [];
            foreach ($_FILES as $field => $file) {
                foreach ((array)$file['tmp_name'] as $key => $tmpName) {
                    $contents[$field][$key] = $tmpName === '' ? null : file_get_contents($tmpName);
                }
            }
            echo json_encode([
                'get' => $_GET,
                'post' => $_POST,
                'cookie' => $_COOKIE,
                'request' => $_REQUEST,
                'files' => $_FILES,
                'contents' => $contents,
                'input' => PhpWorker::input(),
                'input_base64' => base64_encode(PhpWorker::input()),
                'server' => array_intersect_key($_SERVER, array_flip([
                    'REQUEST_METHOD', 'REQUEST_URI', 'QUERY_STRING', 'CONTENT_TYPE', 'HTTP_X_CUSTOM', 'SCRIPT_NAME', 'PATH', 'PROJECT_ROOT',
                ])),
            ], JSON_INVALID_UTF8_SUBSTITUTE);
            break;

        case '/throw':
            throw new RuntimeException('uncaught-marker');

        case '/warning':
            echo $undefinedVariable;
            echo 'warned';
            break;

        case '/error-log':
            error_log('error-log-marker');
            break;

        case '/allocate':
            $memory = str_repeat('a', 5 << 20);
            echo strlen($memory);
            break;

        case '/headers':
            http_response_code(201);
            header('Content-Type: application/json');
            header('X-Test: first');
            header('X-Test: second');
            header('Set-Cookie: a=1', false);
            header('Set-Cookie: b=2', false);
            echo '{}';
            break;

        case '/redirect':
            header('Location: /count');
            break;

        case '/header-functions':
            header('X-Keep: 1');
            header('X-Drop: 1');
            header('X-Multi: 1');
            header('X-Multi: 2', false);
            header_remove('x-drop');
            header('X-Injected: a' . "\r\n" . 'X-Evil: 1');
            header('X-Teapot: yes', true, 418);
            Vendor\Lib\Emitter::emit();
            $previous = http_response_code();
            echo json_encode(['list' => headers_list(), 'sent' => headers_sent(), 'previous' => $previous]);
            break;

        case '/cookies':
            setcookie('plain', 'a b+c/é');
            setcookie('gone', '');
            setcookie('full', 'v', ['expires' => 2000000000, 'path' => '/p', 'domain' => 'ex.com', 'secure' => true, 'httponly' => true, 'samesite' => 'Lax', 'partitioned' => true]);
            setcookie('old', 'v', 2000000000, '/', '', false, true);
            setrawcookie('raw', 'a%20b');
            try {
                setcookie('bad name', 'v');
            } catch (ValueError $error) {
                echo $error->getMessage();
            }
            break;

        case '/upload':
            $tmpName = $_FILES['file']['tmp_name'];
            echo json_encode([
                'is_uploaded' => is_uploaded_file($tmpName),
                'other_file_is_uploaded' => is_uploaded_file(__FILE__),
                'move_other_file' => move_uploaded_file(__FILE__, $_GET['to'] . '/stolen'),
                'moved' => move_uploaded_file($tmpName, $_GET['to'] . '/' . $_FILES['file']['name']),
                'moved_twice' => move_uploaded_file($tmpName, $_GET['to'] . '/again'),
            ]);
            break;

        case '/session':
            session_start();
            $_SESSION['visits'] = ($_SESSION['visits'] ?? 0) + 1;
            if (isset($_GET['regenerate'])) {
                session_regenerate_id(true);
            }
            if (isset($_GET['close'])) {
                session_write_close();
            }
            if (isset($_GET['renew'])) {
                // Like CakePHP's Session::renew(): delete the old cookie, then regenerate the id
                setcookie(session_name(), '', ['expires' => time() - 42000, 'path' => '/']);
                session_regenerate_id(true);
            }
            if (isset($_GET['own-cookie'])) {
                // Like Symfony, which sends the session cookie itself
                setcookie(session_name(), session_id(), ['path' => '/', 'samesite' => 'lax']);
            }
            echo json_encode(['visits' => $_SESSION['visits'], 'id' => session_id()]);
            break;

        case '/no-session':
            echo json_encode(['session_set' => isset($_SESSION), 'status' => session_status()]);
            break;

        case '/close-buffers':
            // What CodeIgniter's app/Config/Events.php does on every request
            echo 'a';
            while (ob_get_level() > 0) {
                ob_end_flush();
            }
            ob_start(static fn(string $buffer): string => $buffer);
            echo 'bc';
            header('X-After-Output: yes');
            break;

        case '/level-zero':
            // Every buffer closed and output before opening a new one: PHP's output layer gets it
            while (ob_get_level() > 0) {
                ob_end_flush();
            }
            echo 'level zero';
            break;

        case '/large':
            for ($i = 0; $i < 1000; $i++) {
                echo str_pad((string)$i, 10000, '.'), "\n";
            }
            break;

        case '/binary':
            echo base64_decode($_GET['data']);
            break;

        case '/nested-buffer':
            echo 'outer ';
            ob_start();
            echo 'inner';
            break;

        case '/exception':
            echo 'partial';
            throw new RuntimeException('boom');

        case '/exit':
            echo 'bye';
            exit;

        case '/fatal':
            ini_set('memory_limit', '8M');
            str_repeat('x', 64 << 20);
            break;

        case '/memory-full':
            // Memory really full when the fatal error happens, as in a leaking application
            ini_set('memory_limit', '16M');
            echo str_repeat('p', 2 << 20); // a large page already buffered, like a framework error page
            $chunks = [];
            while (true) {
                $chunks[] = str_repeat('x', 1 << 16);
            }

        case '/sleep':
            sleep(5);
            break;

        default:
            http_response_code(404);
    }
};
