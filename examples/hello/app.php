<?php

// -mode worker (the default): this part runs ONCE per worker. Anything built here stays in memory.
$bootedAt = date('c');
$requests = 0;

// ...and this closure runs on every request
return static function () use ($bootedAt, &$requests): void {
    $requests++;
    header('Content-Type: application/json');
    echo json_encode([
        'pid' => getmypid(),
        'booted_at' => $bootedAt,
        'requests_served' => $requests,
        'get' => $_GET,
        'post' => $_POST,
    ]);
};
