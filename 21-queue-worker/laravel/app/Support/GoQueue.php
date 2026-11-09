<?php

namespace App\Support;

use Illuminate\Support\Facades\Redis;
use Illuminate\Support\Str;

/**
 * Sends work to the Go worker. Use it like dispatch():
 *
 *     $id = GoQueue::push('sum', ['numbers' => [1, 2, 3.5]]);
 *
 * Store $id if you need to match the result to something later.
 */
class GoQueue
{
    public static function push(string $type, array $payload = [], int $maxAttempts = 3): string
    {
        $id = (string) Str::uuid();

        // Laravel adds its Redis key prefix here automatically, so this
        // writes to "laravel-database-go:jobs" by default. The Go worker's
        // -prefix flag has to match.
        Redis::connection()->rpush('go:jobs', json_encode([
            'id' => $id,
            'type' => $type,
            // Without the cast, an empty array encodes as [] instead of {},
            // and Go can't decode [] into a struct.
            'payload' => (object) $payload,
            'attempts' => 0,
            'max_attempts' => $maxAttempts,
        ], JSON_THROW_ON_ERROR));

        return $id;
    }
}
