<?php

function greeting(string $name): string
{
    return '<h1>Hello, ' . htmlspecialchars($name) . '</h1>';
}
