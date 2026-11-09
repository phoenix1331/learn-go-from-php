<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('customers', function (Blueprint $table) {
            $table->id();
            // The Go importer's upsert depends on this unique index.
            $table->string('email')->unique();
            $table->string('name');
            $table->string('country', 2);
            $table->string('plan');
            $table->date('signed_up');
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('customers');
    }
};
