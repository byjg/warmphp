<?php

// -mode legacy: this file is included on every request, like a classic front controller, in a
// worker process that stays: what it loads with require_once (greeting.php) is loaded only once.
// Declare functions and classes in files loaded with require_once, never here.
require_once __DIR__ . '/greeting.php';

header('Content-Type: text/html; charset=utf-8');
echo greeting($_GET['name'] ?? 'world');
