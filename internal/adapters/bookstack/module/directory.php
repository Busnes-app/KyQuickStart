<?php
// SPDX-License-Identifier: MIT

namespace KyQuickStart\BookStack;

use BookStack\Facades\Theme;
use BookStack\Users\Models\User;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use RuntimeException;
use Throwable;

final class Directory
{
    public const CONTRACT = 'ky.bookstack-directory/1';

    public static function validate(array $data): array
    {
        $keys = array_keys($data); sort($keys);
        if ($keys !== ['contract', 'profile', 'roles'] || $data['contract'] !== self::CONTRACT ||
            !is_array($data['profile']) || !is_array($data['roles'])) {
            throw new RuntimeException('Invalid directory request');
        }
        $profile = Profiles::validate($data['profile']);
        $roles = Roles::validate($data['roles']);
        foreach (['action', 'user_id', 'setup_id', 'issuer', 'client_id', 'subject'] as $field) {
            if ($profile[$field] !== $roles[$field]) {
                throw new RuntimeException('One owned directory identity required');
            }
        }
        return ['contract' => self::CONTRACT, 'profile' => $profile, 'roles' => $roles];
    }

    public static function apply(array $request): array
    {
        // Both native commands join this transaction. A role/audit refusal must
        // also roll back the preceding profile update or initialization.
        return DB::transaction(function () use ($request): array {
            User::query()->whereKey($request['profile']['user_id'])->lockForUpdate()->firstOrFail();
            $profile = Profiles::apply($request['profile']);
            $roles = Roles::apply($request['roles']);
            if ($profile['disabled'] !== $roles['disabled']) {
                throw new RuntimeException('Directory lifecycle changed');
            }
            return ['contract' => self::CONTRACT, 'module_version' => Revocations::VERSION,
                'profile' => $profile, 'roles' => $roles];
        }, 3);
    }
}

Theme::registerCommand(new class extends Command {
    protected $signature = 'kyquickstart:directory {--request= : Canonical private JSON request file}';
    protected $description = 'Atomically inspect or project an exact owned native profile and roles';

    public function handle(): int
    {
        try {
            $result = Directory::apply(Directory::validate(Revocations::readPrivateRequest((string) $this->option('request'))));
            Log::notice('kyquickstart directory', ['success' => true,
                'user_id' => $result['profile']['user_id'], 'profile_revision' => $result['profile']['revision'],
                'role_revision' => $result['roles']['revision']]);
            $this->line(json_encode($result, JSON_THROW_ON_ERROR));
            return 0;
        } catch (Throwable $error) {
            Log::notice('kyquickstart directory', ['success' => false]);
            $this->error('Directory request rejected');
            return 1;
        }
    }
});
