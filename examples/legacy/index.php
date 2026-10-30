<?php

// Script mode: this file is included on every request, like a classic front controller.
// Declare functions and classes in files loaded with require_once, never here.
require_once __DIR__ . '/greeting.php';

header('Content-Type: text/html; charset=utf-8');
echo greeting($_GET['name'] ?? 'world');
