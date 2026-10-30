<?php

namespace Vendor\Lib;

// Library code calling header() unqualified from a namespace, like most Composer packages
class Emitter
{
    public static function emit(): void
    {
        header('X-Namespaced: yes');
    }
}
