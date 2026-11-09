<?php

namespace App\Queue;

use Illuminate\Contracts\Queue\Job;
use Illuminate\Support\Facades\Log;

/**
 * Receives results from the Go worker. Run it with a normal queue worker:
 *
 *     php artisan queue:work redis --queue=go-results
 *
 * Go names this class in the "job" field of the payload it writes, so
 * renaming it means changing laravelPayload in queue.go as well.
 */
class GoResultHandler
{
    public function handle(Job $job, array $data): void
    {
        // A crash in Go can deliver the same result twice. In a real app, make
        // this safe to repeat, e.g. update a row by $data['job_id'].
        if ($data['status'] === 'done') {
            Log::info('Go job finished', $data);
        } else {
            Log::error('Go job failed', $data);
        }

        // Handlers in this raw style must delete the job themselves.
        // Without this, Laravel runs it again after retry_after.
        $job->delete();
    }
}
