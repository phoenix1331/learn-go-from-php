<?php

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Facades\Process;
use Illuminate\Support\Str;

/**
 * Build the binary first, from the Go repo:
 *
 *     go build -o /path/to/laravel/bin/csvimport ./22-csv-importer
 *
 * Then: php artisan customers:import customers.csv [--dry-run]
 */
class ImportCustomers extends Command
{
    protected $signature = 'customers:import {file} {--dry-run}';

    protected $description = 'Import a customers CSV using the Go importer';

    public function handle(): int
    {
        $rejects = storage_path('app/rejects-'.Str::uuid().'.csv');

        // An array is passed straight to the program with no shell involved,
        // so a strange filename can't turn into a command.
        $command = [
            base_path('bin/csvimport'),
            '-db='.config('database.connections.sqlite.database'),
            '-rejects='.$rejects,
            // Less than Process::timeout, so Go can stop cleanly and report.
            // Laravel's timeout sends SIGKILL almost at once.
            '-timeout=55m',
        ];
        if ($this->option('dry-run')) {
            $command[] = '-dry-run';
        }
        $command[] = $this->argument('file');

        $partial = '';
        $result = Process::timeout(3600)->run($command, function (string $type, string $output) use (&$partial) {
            if ($type !== 'err') {
                return;
            }
            // Output arrives in chunks that may end part way through a line.
            $lines = explode("\n", $partial.$output);
            $partial = array_pop($lines);
            foreach ($lines as $line) {
                $log = json_decode($line, true);
                if (($log['msg'] ?? null) === 'progress') {
                    $this->line("  {$log['rows']} rows read...");
                }
            }
        });

        $summary = json_decode($result->output(), true);
        if (! is_array($summary)) {
            // Only exit 2 (a bug in the flags above) gets here.
            $this->error('Importer did not report a result: '.$result->errorOutput());

            return self::FAILURE;
        }

        // The exit code says what kind of outcome it was.
        match ($result->exitCode()) {
            0 => $this->info("Imported {$summary['imported']} rows."),
            3 => $this->warn("Imported {$summary['imported']} rows, rejected {$summary['rejected']}. See {$summary['rejects_file']}"),
            4 => $this->warn("Stopped early ({$summary['status']}) after {$summary['imported']} rows: {$summary['error']}"),
            default => $this->error('Import failed: '.($summary['error'] ?? 'unknown error')),
        };

        foreach ($summary['first_rejects'] as $reject) {
            $this->line("  line {$reject['line']}: ".implode('; ', $reject['errors']));
        }

        return $result->exitCode() === 0 ? self::SUCCESS : self::FAILURE;
    }
}
