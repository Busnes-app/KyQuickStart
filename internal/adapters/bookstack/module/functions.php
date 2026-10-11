<?php
// SPDX-License-Identifier: MIT

namespace KyQuickStart\BookStack;

use BookStack\Facades\Theme;
use BookStack\Theming\ThemeEvents;
use BookStack\Users\Models\User;
use Illuminate\Console\Command;
use Illuminate\Contracts\Http\Kernel;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use RuntimeException;
use Throwable;

final class Revocations
{
    public const CONTRACT = 'ky.bookstack-offboard/1';
    public const VERSION = '0.15.0';

    private static function effectiveUid(): int
    {
        // Target containers are Linux; the pinned PHP image has no POSIX extension.
        $status = file_get_contents('/proc/self/status', false, null, 0, 8192);
        if (!$status || !preg_match('/^Uid:\s+\d+\s+(\d+)\s+/m', $status, $match)) {
            throw new RuntimeException('Application user unavailable');
        }
        return (int) $match[1];
    }

    public static function directory(): string
    {
        $path = (string) env('KYQ_BOOKSTACK_REVOCATION_DIR', '');
        if (!str_starts_with($path, '/') || realpath($path) !== $path) {
            throw new RuntimeException('Canonical persistent revocation directory required');
        }
        self::outsidePublic($path);
        self::privatePath($path, true);
        return $path;
    }

    public static function outsidePublic(string $path): void
    {
        foreach ([public_path(), public_path('uploads')] as $public) {
            $public = realpath($public);
            if ($public && ($path === $public || str_starts_with($path, $public . '/'))) {
                throw new RuntimeException('Revocation storage must be outside public files');
            }
        }
    }

    private static function privatePath(string $path, bool $directory): void
    {
        clearstatcache(true, $path);
        $stat = @lstat($path);
        $type = $directory ? 0040000 : 0100000;
        $mode = $directory ? 0700 : 0600;
        if (!$stat || ($stat['mode'] & 0170000) !== $type || ($stat['mode'] & 0777) !== $mode || $stat['uid'] !== self::effectiveUid()) {
            throw new RuntimeException('Private application-owned path required');
        }
        for ($parent = dirname($path);; $parent = dirname($parent)) {
            $stat = @lstat($parent);
            if (!$stat || ($stat['mode'] & 0170000) !== 0040000 || ($stat['mode'] & 0022) !== 0 ||
                !in_array($stat['uid'], [0, self::effectiveUid()], true)) {
                throw new RuntimeException('Protected path ancestors required');
            }
            if ($parent === '/') {
                break;
            }
        }
    }

    private static function subjectFile(string $issuer, string $subject): string
    {
        return 'subject-' . hash('sha256', $issuer . "\0" . $subject) . '.json';
    }

    private static function exists(string $path): bool
    {
        clearstatcache(true, $path);
        return @lstat($path) !== false;
    }

    public static function denied(User $user): bool
    {
        $path = self::directory();
        return self::exists($path . '/user-' . $user->id . '.json') || self::subjectDenied((string) $user->external_auth_id);
    }

    public static function subjectDenied(string $subject): bool
    {
        $issuer = (string) config('oidc.issuer');
        return $subject !== '' && $issuer !== '' && self::exists(self::directory() . '/' . self::subjectFile($issuer, $subject));
    }

    public static function readPrivateRequest(string $path): array
    {
        if (!str_starts_with($path, '/') || realpath($path) !== $path) {
            throw new RuntimeException('Canonical absolute request path required');
        }
        self::privatePath(dirname($path), true);
        self::privatePath($path, false);
        $file = @fopen($path, 'rb');
        if (!$file) {
            throw new RuntimeException('Request unavailable');
        }
        try {
            $stat = fstat($file);
            if (($stat['mode'] & 0170000) !== 0100000 || ($stat['mode'] & 0777) !== 0600 ||
                $stat['uid'] !== self::effectiveUid() || $stat['size'] > 4096) {
                throw new RuntimeException('Invalid private request');
            }
            $body = stream_get_contents($file, 4097);
            if (strlen($body) > 4096) {
                throw new RuntimeException('Request too large');
            }
            $data = json_decode($body, true, 8, JSON_THROW_ON_ERROR);
        } finally {
            fclose($file);
        }
        if (!is_array($data)) {
            throw new RuntimeException('Invalid request');
        }
        return $data;
    }

    public static function request(string $path): array
    {
        return self::validateRequest(self::readPrivateRequest($path));
    }

    public static function validateRequest(array $data): array
    {
        $keys = array_keys($data);
        sort($keys);
        if ($keys !== ['action', 'contract', 'issuer', 'subject', 'user_id'] || $data['contract'] !== self::CONTRACT ||
            !in_array($data['action'], ['check', 'deprovision'], true) || !is_int($data['user_id']) || $data['user_id'] < 1 ||
            !is_string($data['issuer']) || strlen($data['issuer']) > 2048 || !is_string($data['subject']) ||
            strlen($data['subject']) < 1 || strlen($data['subject']) > 255 || preg_match('/[\x00-\x20\x7f]/', $data['subject'])) {
            throw new RuntimeException('Invalid request');
        }
        $url = parse_url($data['issuer']);
        if (!$url || ($url['scheme'] ?? '') !== 'https' || empty($url['host']) ||
            isset($url['user']) || isset($url['pass']) || isset($url['query']) || isset($url['fragment']) ||
            preg_match('/[\x00-\x20\x7f\\\\]/', $data['issuer']) ||
            $data['issuer'] !== config('oidc.issuer') || config('oidc.external_id_claim') !== 'sub') {
            throw new RuntimeException('Configured OIDC issuer binding required');
        }
        return $data;
    }

    public static function apply(array $request): array
    {
        return DB::transaction(function () use ($request): array {
            $user = User::query()->whereKey($request['user_id'])->lockForUpdate()->first();
            if (!$user || $user->isGuest() || $user->external_auth_id !== $request['subject']) {
                throw new RuntimeException('Native account binding mismatch');
            }
            $record = json_encode([
                'contract' => self::CONTRACT, 'user_id' => $user->id,
                'issuer' => $request['issuer'], 'subject' => $request['subject'],
            ], JSON_THROW_ON_ERROR) . "\n";
            $directory = self::directory();
            $paths = [$directory . '/user-' . $user->id . '.json', $directory . '/' . self::subjectFile($request['issuer'], $request['subject'])];
            foreach ($paths as $path) {
                if (self::exists($path)) {
                    self::privatePath($path, false);
                    if (file_get_contents($path, false, null, 0, 4097) !== $record) {
                        throw new RuntimeException('Immutable revocation binding conflict');
                    }
                }
            }
            if ($request['action'] === 'deprovision') {
                // Both immutable fences precede token mutation. A partial publication fails closed
                // and a retry completes it; no other account's state file is rewritten.
                foreach ($paths as $path) {
                    self::publish($path, $record);
                }
                $user->apiTokens()->delete();
            }
            return [
                'contract' => self::CONTRACT, 'module_version' => self::VERSION,
                'user_id' => $user->id, 'disabled' => self::denied($user),
                'binding_digest' => hash('sha256', $request['issuer'] . "\0" . $request['subject']),
                'revocation_complete' => self::exists($paths[0]) && self::exists($paths[1]),
                'tokens_remaining' => $user->apiTokens()->count(),
            ];
        });
    }

    private static function publish(string $path, string $record): void
    {
        if (self::exists($path)) {
            return;
        }
        $temporary = dirname($path) . '/pending-' . bin2hex(random_bytes(16));
        $oldMask = umask(0077);
        try {
            $file = fopen($temporary, 'xb');
        } finally {
            umask($oldMask);
        }
        try {
            if (fwrite($file, $record) !== strlen($record) || !fflush($file) || !fsync($file)) {
                throw new RuntimeException('Revocation persistence failed');
            }
            fclose($file);
            $file = null;
            // Hard-link publication cannot overwrite a concurrently published binding.
            if (!@link($temporary, $path)) {
                self::privatePath($path, false);
                if (file_get_contents($path, false, null, 0, 4097) !== $record) {
                    throw new RuntimeException('Revocation publication conflict');
                }
            }
            $directory = fopen(dirname($path), 'r');
            try {
                if (!fsync($directory)) {
                    throw new RuntimeException('Revocation directory persistence failed');
                }
            } finally {
                fclose($directory);
            }
        } finally {
            if (is_resource($file)) {
                fclose($file);
            }
            @unlink($temporary);
        }
    }
}

final class RevocationMiddleware
{
    public function handle($request, \Closure $next)
    {
        $user = auth()->user();
        if ($user && Revocations::denied($user)) {
            abort(403, 'Account access revoked');
        }
        if ($user) {
            try { Roles::inspectUser($user); } catch (Throwable $error) { abort(403, 'Managed authority unavailable'); }
        }
        return $next($request);
    }
}

Theme::listen(ThemeEvents::APP_BOOT, function ($app): void {
    $kernel = $app->make(Kernel::class);
    foreach (['web', 'api'] as $group) {
        $kernel->appendMiddlewareToGroup($group, RevocationMiddleware::class);
    }
});

Theme::listen(ThemeEvents::AUTH_LOGIN, function (string $method, User $user): void {
    if (Revocations::denied($user)) {
        auth()->logout();
        session()->invalidate();
        abort(403, 'Account access revoked');
    }
});

Theme::listen(ThemeEvents::AUTH_PRE_REGISTER, function (string $method, array $data): ?bool {
    return Revocations::subjectDenied((string) ($data['external_auth_id'] ?? '')) ? false : null;
});

Theme::registerCommand(new class extends Command {
    protected $signature = 'kyquickstart:offboard {--request= : Canonical private JSON request file}';
    protected $description = 'Inspect or revoke an exact retained native account binding';

    public function handle(): int
    {
        try {
            $request = Revocations::request((string) $this->option('request'));
            $result = Revocations::apply($request);
            Log::notice('kyquickstart offboarding', [
                'action' => $request['action'], 'user_id' => $request['user_id'],
                'binding_digest' => hash('sha256', $request['issuer'] . "\0" . $request['subject']), 'success' => true,
            ]);
            $this->line(json_encode($result, JSON_THROW_ON_ERROR));
            return 0;
        } catch (Throwable $error) {
            Log::notice('kyquickstart offboarding', ['success' => false]);
            $this->error('Offboarding rejected');
            return 1;
        }
    }
});

require_once __DIR__ . '/accounts.php';
require_once __DIR__ . '/http.php';

require_once __DIR__ . '/profiles.php';

require_once __DIR__ . '/roles.php';
require_once __DIR__ . '/directory.php';

require_once __DIR__ . '/creation.php';
