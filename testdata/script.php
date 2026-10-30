<?php

// Included on every request: locals start fresh, globals live as long as the process
$GLOBALS['hits'] = ($GLOBALS['hits'] ?? 0) + 1;
echo json_encode(['name' => $_GET['name'] ?? null, 'hits' => $GLOBALS['hits'], 'leaked' => isset($local)]);
$local = true;
