<?php
// SPDX-License-Identifier: MIT

namespace KyQuickStart\BookStack;

use BookStack\Facades\Theme;
use BookStack\Settings\Setting;
use BookStack\Theming\ThemeEvents;
use BookStack\Users\Models\User;
use BookStack\Users\UserRepo;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use RuntimeException;
use Throwable;

final class Profiles
{
    public const CONTRACT = 'ky.bookstack-profile/1';

    public static function key(int $id): string
    {
        return 'kyquickstart-profile:' . $id;
    }

    public static function request(string $path): array
    {
        return self::validate(Revocations::readPrivateRequest($path));
    }

    public static function validate(array $data): array
    {
        $keys = array_keys($data);
        sort($keys);
        if ($keys !== ['action', 'client_id', 'contract', 'email', 'expected_revision', 'issuer', 'name', 'setup_id', 'subject', 'user_id'] ||
            $data['contract'] !== self::CONTRACT || !in_array($data['action'], ['initialize', 'check', 'apply'], true) ||
            !is_int($data['user_id']) || $data['user_id'] <= 0 || !is_int($data['expected_revision']) ||
            $data['expected_revision'] < 0 || $data['expected_revision'] > 9007199254740990) {
            throw new RuntimeException('Invalid profile request');
        }
        // Reuse the account boundary for source, subject, setup and profile fields.
        Accounts::validate(['contract' => Accounts::CONTRACT, 'action' => 'check',
            'administrator' => false, 'setup_id' => $data['setup_id'], 'issuer' => $data['issuer'],
            'client_id' => $data['client_id'], 'subject' => $data['subject'], 'name' => $data['name'], 'email' => $data['email']]);
        return $data;
    }

    public static function apply(array $request): array
    {
        return DB::transaction(function () use ($request): array {
            $owned = Accounts::ownedUser($request['subject']);
            $user = User::query()->whereKey($request['user_id'])->lockForUpdate()->first();
            $primary = Setting::query()->where('setting_key', 'kyquickstart-account:' . $request['setup_id'])->first();
            $ownership = $primary ? json_decode($primary->value, true, 8, JSON_THROW_ON_ERROR) : null;
            if (!$user || $owned->id !== $user->id || $user->external_auth_id !== $request['subject'] ||
                !is_array($ownership) || ($ownership['contract'] ?? '') !== Accounts::CONTRACT || ($ownership['user_id'] ?? null) !== $user->id) {
                throw new RuntimeException('Owned profile binding required');
            }
            $binding = hash('sha256', $request['issuer'] . "\0" . $request['client_id'] . "\0" . $request['subject']);
            $key = self::key($user->id);
            $stored = Setting::query()->where('setting_key', $key)->lockForUpdate()->first();
            $receipt = $stored ? json_decode($stored->value, true, 8, JSON_THROW_ON_ERROR) : null;
            $disabled = Revocations::denied($user);
            if ($stored) {
                if (!is_array($receipt) || ($receipt['contract'] ?? '') !== self::CONTRACT ||
                    ($receipt['user_id'] ?? null) !== $user->id || ($receipt['setup_id'] ?? '') !== $request['setup_id'] ||
                    ($receipt['binding_digest'] ?? '') !== $binding || !is_int($receipt['revision'] ?? null) ||
                    $receipt['revision'] < 0 || ($receipt['name'] ?? null) !== $user->name || ($receipt['email'] ?? null) !== $user->email) {
                    throw new RuntimeException('Native profile drift');
                }
                $audit = Setting::query()->where('setting_key', $key . ':audit:' . $receipt['revision'])->lockForUpdate()->first();
                if (!$audit || $audit->value !== $stored->value) {
                    throw new RuntimeException('Native profile audit proof required');
                }
            } elseif ($request['action'] !== 'initialize' || $disabled || $request['expected_revision'] !== 0 ||
                $request['name'] !== $user->name || $request['email'] !== $user->email) {
                throw new RuntimeException('Explicit current-profile initialization required');
            } else {
                $receipt = ['contract' => self::CONTRACT, 'setup_id' => $request['setup_id'],
                    'user_id' => $user->id, 'binding_digest' => $binding, 'revision' => 0,
                    'name' => $user->name, 'email' => $user->email];
                self::commit($key, $receipt);
            }
            if ($request['action'] !== 'check') {
                if ($disabled || $receipt['revision'] !== $request['expected_revision'] ||
                    ($request['action'] === 'initialize' && ($request['name'] !== $user->name || $request['email'] !== $user->email))) {
                    throw new RuntimeException('Profile revision or lifecycle conflict');
                }
                if ($request['name'] !== $user->name || $request['email'] !== $user->email) {
                    if (User::query()->where('email', $request['email'])->where('id', '!=', $user->id)->exists()) {
                        throw new RuntimeException('Profile email collision');
                    }
                    app(UserRepo::class)->applyOwnedProfile($user, $request['name'], $request['email']);
                    $receipt['name'] = $user->name;
                    $receipt['email'] = $user->email;
                    $receipt['revision']++;
                    self::commit($key, $receipt);
                }
            }
            return $receipt + ['module_version' => Revocations::VERSION, 'disabled' => $disabled];
        }, 3);
    }

    private static function commit(string $key, array $receipt): void
    {
        $value = json_encode($receipt, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR);
        $audit = new Setting();
        $audit->incrementing = false;
        $audit->setting_key = $key . ':audit:' . $receipt['revision'];
        $audit->type = 'string';
        $audit->value = $value;
        $audit->save();
        Setting::query()->upsert([['setting_key' => $key, 'type' => 'string', 'value' => $value]],
            ['setting_key'], ['type', 'value', 'updated_at']);
    }
}

// Route native profile writers through one shared boundary. Locale, theme,
// passwords retain native behavior; separately initialized roles are guarded.
final class ProfileUserRepo extends UserRepo
{
    public function updateWithoutActivity(User $user, array $data, bool $manageUsersAllowed): User
    {
        return DB::transaction(function () use ($user, $data, $manageUsersAllowed): User {
            User::query()->whereKey($user->id)->lockForUpdate()->firstOrFail();
            $user->refresh();
            if (Setting::query()->where('setting_key', Profiles::key($user->id))->exists()) {
                foreach (['name', 'email', 'external_auth_id'] as $field) {
                    if (isset($data[$field]) && $data[$field] !== $user->$field) {
                        throw new \Symfony\Component\HttpKernel\Exception\HttpException(403, 'Profile is directory managed');
                    }
                }
            }
            if (Setting::query()->where('setting_key', Roles::key($user->id))->exists() &&
                (array_key_exists('roles', $data) || (isset($data['external_auth_id']) && $data['external_auth_id'] !== $user->external_auth_id))) {
                throw new \Symfony\Component\HttpKernel\Exception\HttpException(403, 'Roles are directory managed');
            }
            return parent::updateWithoutActivity($user, $data, $manageUsersAllowed);
        }, 3);
    }

    public function destroy(User $user, ?int $newOwnerId = null): void
    {
        DB::transaction(function () use ($user, $newOwnerId): void {
            User::query()->whereKey($user->id)->lockForUpdate()->firstOrFail();
            if (Setting::query()->where('setting_key', Profiles::key($user->id))->exists() ||
                Setting::query()->where('setting_key', Roles::key($user->id))->exists()) {
                throw new \Symfony\Component\HttpKernel\Exception\HttpException(403, 'Profile is directory managed');
            }
            parent::destroy($user, $newOwnerId);
        }, 3);
    }

    public function applyOwnedRoles(User $user, array $ids): User
    {
        return parent::updateWithoutActivity($user, ['roles' => $ids], true);
    }

    public function applyOwnedProfile(User $user, string $name, string $email): User
    {
        return parent::updateWithoutActivity($user, ['name' => $name, 'email' => $email], true);
    }
}

Theme::listen(ThemeEvents::APP_BOOT, function ($app): void {
    $app->bind(UserRepo::class, ProfileUserRepo::class);
});

Theme::registerCommand(new class extends Command {
    protected $signature = 'kyquickstart:profile {--request= : Canonical private JSON request file}';
    protected $description = 'Inspect or project an exact receipt-owned native profile';

    public function handle(): int
    {
        try {
            $result = Profiles::apply(Profiles::request((string) $this->option('request')));
            Log::notice('kyquickstart profile', ['success' => true, 'user_id' => $result['user_id'], 'revision' => $result['revision']]);
            $this->line(json_encode($result, JSON_THROW_ON_ERROR));
            return 0;
        } catch (Throwable $error) {
            Log::notice('kyquickstart profile', ['success' => false]);
            $this->error('Profile request rejected');
            return 1;
        }
    }
});
