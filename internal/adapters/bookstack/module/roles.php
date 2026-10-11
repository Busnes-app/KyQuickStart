<?php
// SPDX-License-Identifier: MIT

namespace KyQuickStart\BookStack;

use BookStack\Facades\Theme;
use BookStack\Settings\Setting;
use BookStack\Users\Models\Role;
use BookStack\Users\Models\User;
use BookStack\Users\UserRepo;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use RuntimeException;
use Throwable;

final class Roles
{
    public const CONTRACT = 'ky.bookstack-roles/1';

    public static function key(int $id): string { return 'kyquickstart-roles:' . $id; }

    public static function request(string $path): array
    {
        return self::validate(Revocations::readPrivateRequest($path));
    }

    public static function validate(array $data): array
    {
        $keys = array_keys($data); sort($keys);
        if ($keys !== ['action', 'administrator', 'client_id', 'contract', 'everyday_role', 'expected_revision', 'issuer', 'ordinary_role_ids', 'setup_id', 'subject', 'user_id'] ||
            $data['contract'] !== self::CONTRACT || !in_array($data['action'], ['initialize', 'check', 'apply'], true) ||
            !is_bool($data['administrator']) || !is_int($data['user_id']) || $data['user_id'] < 1 ||
            !is_int($data['expected_revision']) || $data['expected_revision'] < 0 || $data['expected_revision'] > 9007199254740990 ||
            !is_string($data['everyday_role']) || !preg_match('/^[a-z][a-z0-9_-]{0,63}$/D', $data['everyday_role']) ||
            $data['everyday_role'] === 'admin' || !is_array($data['ordinary_role_ids']) || !array_is_list($data['ordinary_role_ids']) ||
            count($data['ordinary_role_ids']) > 32) {
            throw new RuntimeException('Invalid role request');
        }
        $previous = 0;
        foreach ($data['ordinary_role_ids'] as $id) {
            if (!is_int($id) || $id <= $previous) { throw new RuntimeException('Canonical role IDs required'); }
            $previous = $id;
        }
        Accounts::validate(['contract' => Accounts::CONTRACT, 'action' => 'check', 'administrator' => false,
            'setup_id' => $data['setup_id'], 'issuer' => $data['issuer'], 'client_id' => $data['client_id'],
            'subject' => $data['subject'], 'name' => 'Role inspection', 'email' => 'inspection@example.test']);
        return $data;
    }

    private static function policy(array $request): array
    {
        $admin = Role::getSystemRole('admin');
        if (!$admin || in_array($admin->id, $request['ordinary_role_ids'], true)) {
            throw new RuntimeException('Native administrator role required');
        }
        $ids = $request['ordinary_role_ids']; $ids[] = $admin->id; sort($ids, SORT_NUMERIC);
        $proofs = [];
        $default = (int) setting('registration-role');
        foreach ($ids as $id) {
            $role = Role::query()->whereKey($id)->lockForUpdate()->first();
            if (!$role) { throw new RuntimeException('Pinned native role missing'); }
            $permissions = $role->permissions()->pluck('name')->all(); sort($permissions, SORT_STRING);
            if ($id !== $admin->id && ($role->system_name === 'admin' ||
                array_intersect($permissions, ['users-manage', 'user-roles-manage', 'settings-manage']) ||
                ($id !== $default && $role->external_auth_id !== $request['everyday_role'] &&
                    !(!$role->external_auth_id && str_replace(' ', '-', trim(strtolower($role->display_name))) === $request['everyday_role'])))) {
                throw new RuntimeException('Ordinary role policy refused');
            }
            $proofs[] = [$id, $role->system_name, $role->external_auth_id, $role->display_name, (bool) $role->mfa_enforced, $permissions];
        }
        return ['administrator_role_id' => $admin->id, 'policy_digest' => hash('sha256', json_encode(
            [$request['everyday_role'], $default, $proofs], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR))];
    }

    // Called only inside Creation's account/receipt transaction, before any directory receipt.
    public static function initializeCreatedOrdinary(array $request): void
    {
        self::validate($request);
        if ($request['action'] !== 'initialize' || $request['administrator'] || $request['expected_revision'] !== 0) {
            throw new RuntimeException('Ordinary initialization required');
        }
        $user = Accounts::ownedUser($request['subject']);
        if ($user->id !== $request['user_id'] || self::ids($user) !== [] ||
            Setting::query()->whereIn('setting_key', [self::key($user->id), 'kyquickstart-profile:' . $user->id])->exists()) {
            throw new RuntimeException('Uninitialized new account required');
        }
        self::policy($request);
        app(UserRepo::class)->applyOwnedRoles($user, $request['ordinary_role_ids']);
    }

    private static function ids(User $user): array
    {
        return $user->roles()->orderBy('roles.id')->pluck('roles.id')->all();
    }

    public static function apply(array $request): array
    {
        return DB::transaction(function () use ($request): array {
            $owned = Accounts::ownedUser($request['subject']);
            $user = User::query()->whereKey($request['user_id'])->lockForUpdate()->first();
            $primary = Setting::query()->where('setting_key', 'kyquickstart-account:' . $request['setup_id'])->first();
            $ownership = $primary ? json_decode($primary->value, true, 8, JSON_THROW_ON_ERROR) : null;
            if (!$user || $owned->id !== $user->id || !is_array($ownership) ||
                ($ownership['contract'] ?? '') !== Accounts::CONTRACT || ($ownership['user_id'] ?? null) !== $user->id) {
                throw new RuntimeException('Owned role binding required');
            }
            $policy = self::policy($request);
            $binding = hash('sha256', $request['issuer'] . "\0" . $request['client_id'] . "\0" . $request['subject']);
            $key = self::key($user->id);
            $stored = Setting::query()->where('setting_key', $key)->lockForUpdate()->first();
            $receipt = $stored ? json_decode($stored->value, true, 8, JSON_THROW_ON_ERROR) : null;
            $disabled = Revocations::denied($user);
            $desired = $request['ordinary_role_ids'];
            if ($request['administrator']) { $desired[] = $policy['administrator_role_id']; }
            sort($desired, SORT_NUMERIC);
            if ($stored) {
                if (!is_array($receipt) || ($receipt['contract'] ?? '') !== self::CONTRACT ||
                    ($receipt['setup_id'] ?? '') !== $request['setup_id'] || ($receipt['user_id'] ?? null) !== $user->id ||
                    ($receipt['binding_digest'] ?? '') !== $binding || ($receipt['everyday_role'] ?? '') !== $request['everyday_role'] ||
                    ($receipt['ordinary_role_ids'] ?? null) !== $request['ordinary_role_ids'] ||
                    ($receipt['policy_digest'] ?? '') !== $policy['policy_digest'] ||
                    ($receipt['administrator_role_id'] ?? null) !== $policy['administrator_role_id'] ||
                    !is_bool($receipt['administrator'] ?? null) || !is_int($receipt['revision'] ?? null) ||
                    $receipt['revision'] < 0 || $receipt['revision'] > 9007199254740991 ||
                    ($receipt['role_ids'] ?? null) !== self::ids($user) ||
                    $receipt['administrator'] !== in_array($policy['administrator_role_id'], self::ids($user), true)) {
                    throw new RuntimeException('Native role authority drift');
                }
                $audit = Setting::query()->where('setting_key', $key . ':audit:' . $receipt['revision'])->lockForUpdate()->first();
                if (!$audit || $audit->value !== $stored->value) { throw new RuntimeException('Native role audit required'); }
            } elseif ($request['action'] !== 'initialize' || $disabled || $request['expected_revision'] !== 0 || self::ids($user) !== $desired ||
                ($request['administrator'] && !Accounts::verifiedLogin($user, true))) {
                throw new RuntimeException('Explicit current-role initialization required');
            } else {
                $receipt = ['contract' => self::CONTRACT, 'setup_id' => $request['setup_id'], 'user_id' => $user->id,
                    'binding_digest' => $binding, 'revision' => 0, 'administrator' => $request['administrator'],
                    'ordinary_role_ids' => $request['ordinary_role_ids'], 'everyday_role' => $request['everyday_role'],
                    'role_ids' => $desired] + $policy;
                self::commit($key, $receipt);
            }
            if ($request['action'] !== 'check') {
                if ($disabled || $receipt['revision'] !== $request['expected_revision'] ||
                    ($request['action'] === 'initialize' && $receipt['administrator'] !== $request['administrator'])) {
                    throw new RuntimeException('Role revision or lifecycle conflict');
                }
                if ($receipt['administrator'] !== $request['administrator']) {
                    app(UserRepo::class)->applyOwnedRoles($user, $desired);
                    $receipt['administrator'] = $request['administrator']; $receipt['role_ids'] = $desired; $receipt['revision']++;
                    self::commit($key, $receipt);
                }
            }
            return $receipt + ['module_version' => Revocations::VERSION, 'disabled' => $disabled];
        }, 3);
    }

    private static function commit(string $key, array $receipt): void
    {
        $value = json_encode($receipt, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR);
        $audit = new Setting(); $audit->incrementing = false; $audit->setting_key = $key . ':audit:' . $receipt['revision'];
        $audit->type = 'string'; $audit->value = $value; $audit->save();
        Setting::query()->upsert([['setting_key' => $key, 'type' => 'string', 'value' => $value]], ['setting_key'], ['type', 'value', 'updated_at']);
    }

    public static function inspectUser(User $user): ?array
    {
        $stored = Setting::query()->where('setting_key', self::key($user->id))->first();
        if (!$stored) { return null; }
        $receipt = json_decode($stored->value, true, 8, JSON_THROW_ON_ERROR);
        if (!is_array($receipt) || !is_string($receipt['setup_id'] ?? null) || !is_string($receipt['everyday_role'] ?? null) ||
            !is_array($receipt['ordinary_role_ids'] ?? null) || !is_bool($receipt['administrator'] ?? null)) {
            throw new RuntimeException('Owned role receipt required');
        }
        return self::apply(self::validate(['contract' => self::CONTRACT, 'action' => 'check', 'user_id' => $user->id,
            'setup_id' => $receipt['setup_id'], 'issuer' => (string) config('oidc.issuer'), 'client_id' => (string) config('oidc.client_id'),
            'subject' => (string) $user->external_auth_id, 'ordinary_role_ids' => $receipt['ordinary_role_ids'],
            'everyday_role' => $receipt['everyday_role'], 'administrator' => $receipt['administrator'], 'expected_revision' => 0]));
    }

    public static function verifyLogin(User $user, mixed $claims): bool
    {
        $receipt = self::inspectUser($user);
        if ($receipt === null) { return false; }
        if ($receipt['disabled'] || ($claims !== null && (!is_array($claims) || !array_is_list($claims) || count($claims) > 32)) ||
            ($claims !== null && array_filter($claims, fn ($role): bool => !is_string($role) || !in_array($role, ['admin', $receipt['everyday_role']], true))) ||
            count(array_unique($claims ?? [])) !== count($claims ?? []) ||
            in_array('admin', $claims ?? [], true) !== $receipt['administrator']) {
            throw new \BookStack\Access\Oidc\OidcException('Owned role authority conflicts with signed identity');
        }
        return true;
    }
}

Theme::registerCommand(new class extends Command {
    protected $signature = 'kyquickstart:roles {--request= : Canonical private JSON request file}';
    protected $description = 'Inspect or project exact receipt-owned native roles';
    public function handle(): int
    {
        try {
            $result = Roles::apply(Roles::request((string) $this->option('request')));
            Log::notice('kyquickstart roles', ['success' => true, 'user_id' => $result['user_id'], 'revision' => $result['revision']]);
            $this->line(json_encode($result, JSON_THROW_ON_ERROR)); return 0;
        } catch (Throwable $error) {
            $reason = match ($error->getMessage()) {
                'Ordinary role policy refused' => 'ordinary-policy',
                'Explicit current-role initialization required' => 'initialization',
                'Owned role binding required' => 'binding',
                'Invalid role request' => 'request',
                'Native role authority drift' => 'drift',
                'Native role audit required' => 'audit',
                default => 'native-error',
            };
            Log::notice('kyquickstart roles', ['success' => false, 'reason' => $reason]); $this->error('Role request rejected'); return 1;
        }
    }
});
