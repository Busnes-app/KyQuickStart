<?php
// SPDX-License-Identifier: MIT

namespace KyQuickStart\BookStack;

use BookStack\Facades\Theme;
use BookStack\Settings\Setting;
use BookStack\Users\Models\User;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use RuntimeException;
use Throwable;

final class Creation
{
    public const CONTRACT = 'ky.bookstack-creation/1';
    public const SCOPE_CONTRACT = 'ky.bookstack-creation-scope/1';

    public static function validateScope(array $data): array
    {
        $keys = array_keys($data); sort($keys);
        if ($keys !== ['account_setup_id', 'action', 'client_id', 'contract', 'everyday_role', 'installation_id', 'issuer', 'ordinary_role_ids', 'subject'] ||
            $data['contract'] !== self::SCOPE_CONTRACT || !is_string($data['installation_id']) ||
            !preg_match('/^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/D', $data['installation_id']) || !in_array($data['action'], ['subject-probe', 'resolve'], true) ||
            !is_array($data['ordinary_role_ids']) || count($data['ordinary_role_ids']) < 1) {
            throw new RuntimeException('Invalid creation scope');
        }
        Roles::validate(self::scopeRoles($data, $data['account_setup_id'], 1));
        return $data;
    }

    private static function scopeRoles(array $data, string $setup, int $id): array
    {
        return self::roles(['account' => ['setup_id' => $setup, 'issuer' => $data['issuer'],
            'client_id' => $data['client_id'], 'subject' => $data['subject']],
            'everyday_role' => $data['everyday_role'], 'ordinary_role_ids' => $data['ordinary_role_ids']], $id, 'check');
    }

    private static function scopeProof(array $data): array
    {
        return ['module_version' => Revocations::VERSION, 'installation_id' => $data['installation_id'], 'account_setup_id' => $data['account_setup_id'],
            'source_digest' => hash('sha256', $data['issuer'] . "\0" . $data['client_id']),
            'binding_digest' => hash('sha256', $data['issuer'] . "\0" . $data['subject']),
            'everyday_role' => $data['everyday_role'], 'ordinary_role_ids' => $data['ordinary_role_ids']];
    }

    public static function inspectScope(array $data): array
    {
        return DB::transaction(function () use ($data): array {
            $assignmentKey = 'kyquickstart-assignment:' . hash('sha256', $data['issuer'] . "\0" . $data['subject']);
            if ($data['action'] === 'subject-probe') {
                return self::scopeProof($data) + ['contract' => 'ky.bookstack-creation-subject-absence/1',
                    'absent' => !Revocations::subjectDenied($data['subject']) &&
                        !Setting::query()->whereIn('setting_key', [$assignmentKey, 'kyquickstart-account:' . $data['account_setup_id'],
                            'kyquickstart-creation:' . $data['account_setup_id'], 'kyquickstart-creation:' . $data['account_setup_id'] . ':audit'])->exists() &&
                        !User::query()->where('external_auth_id', $data['subject'])->exists()];
            }
            $owned = Accounts::ownedUser($data['subject']);
            $user = User::query()->whereKey($owned->id)->lockForUpdate()->firstOrFail();
            $assignment = Setting::query()->where('setting_key', $assignmentKey)->firstOrFail();
            $assigned = json_decode($assignment->value, true, 8, JSON_THROW_ON_ERROR);
            $setup = $assigned['setup_id'] ?? '';
            if ($setup !== $data['account_setup_id'] || !is_string($setup) || !preg_match('/^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/D', $setup)) {
                throw new RuntimeException('Native setup pin required');
            }
            $primary = Setting::query()->where('setting_key', 'kyquickstart-account:' . $setup)->firstOrFail();
            $account = json_decode($primary->value, true, 8, JSON_THROW_ON_ERROR);
            $key = 'kyquickstart-creation:' . $setup;
            $stored = Setting::query()->where('setting_key', $key)->firstOrFail();
            $audit = Setting::query()->where('setting_key', $key . ':audit')->firstOrFail();
            $receipt = json_decode($stored->value, true, 8, JSON_THROW_ON_ERROR);
            if (!is_array($account) || ($account['contract'] ?? '') !== Accounts::CONTRACT ||
                ($account['user_id'] ?? null) !== $user->id || !is_string($account['intent_digest'] ?? null) ||
                !preg_match('/^[a-f0-9]{64}$/D', $account['intent_digest'])) {
                throw new RuntimeException('Native account intent required');
            }
            $intent = hash('sha256', json_encode([$data['installation_id'], $account['intent_digest'],
                $data['everyday_role'], $data['ordinary_role_ids']], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR));
            if (!is_array($receipt) || ($receipt['contract'] ?? '') !== self::CONTRACT ||
                ($receipt['installation_id'] ?? '') !== $data['installation_id'] ||
                ($receipt['intent_digest'] ?? '') !== $intent || ($receipt['user_id'] ?? null) !== $user->id || $audit->value !== $stored->value) {
                throw new RuntimeException('Native creation ownership required');
            }
            $roles = self::scopeRoles($data, $setup, $user->id);
            $profile = array_diff_key($roles, array_flip(['everyday_role', 'ordinary_role_ids', 'administrator']));
            $profile['contract'] = Profiles::CONTRACT;
            $profile['name'] = $user->name; $profile['email'] = $user->email;
            Directory::apply(Directory::validate(['contract' => Directory::CONTRACT, 'profile' => $profile, 'roles' => $roles]));
            return self::scopeProof($data) + ['contract' => self::SCOPE_CONTRACT, 'user_id' => $user->id,
                'account_setup_id' => $setup, 'account_intent_digest' => $account['intent_digest'], 'creation_intent_digest' => $intent];
        }, 3);
    }

    public static function validate(array $data): array
    {
        $keys = array_keys($data); sort($keys);
        if ($keys !== ['account', 'action', 'contract', 'everyday_role', 'installation_id', 'ordinary_role_ids'] ||
            $data['contract'] !== self::CONTRACT || !in_array($data['action'], ['create', 'check'], true) ||
            !is_string($data['installation_id']) || !preg_match('/^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/D', $data['installation_id']) ||
            !is_array($data['account']) || !is_array($data['ordinary_role_ids']) || count($data['ordinary_role_ids']) < 1) {
            throw new RuntimeException('Invalid ordinary creation request');
        }
        $account = Accounts::validate($data['account']);
        if ($account['administrator'] || $account['action'] !== $data['action']) {
            throw new RuntimeException('Ordinary creation authority required');
        }
        Roles::validate(self::roles($data, 1, 'initialize'));
        return $data;
    }

    private static function roles(array $request, int $id, string $action): array
    {
        $account = $request['account'];
        return ['contract' => Roles::CONTRACT, 'action' => $action, 'user_id' => $id,
            'setup_id' => $account['setup_id'], 'issuer' => $account['issuer'], 'client_id' => $account['client_id'],
            'subject' => $account['subject'], 'everyday_role' => $request['everyday_role'],
            'ordinary_role_ids' => $request['ordinary_role_ids'], 'administrator' => false, 'expected_revision' => 0];
    }

    public static function absence(array $request): array
    {
        $account = $request['account'];
        $assignment = 'kyquickstart-assignment:' . hash('sha256', $account['issuer'] . "\0" . $account['subject']);
        $absent = !Revocations::subjectDenied($account['subject']) &&
            !Setting::query()->whereIn('setting_key', ['kyquickstart-creation:' . $account['setup_id'],
                'kyquickstart-account:' . $account['setup_id'], $assignment])->exists() &&
            !User::query()->where('external_auth_id', $account['subject'])->orWhere('email', $account['email'])->exists();
        return ['contract' => 'ky.bookstack-creation-absence/1', 'module_version' => Revocations::VERSION,
            'installation_id' => $request['installation_id'], 'account_setup_id' => $account['setup_id'],
            'source_digest' => hash('sha256', $account['issuer'] . "\0" . $account['client_id']),
            'binding_digest' => hash('sha256', $account['issuer'] . "\0" . $account['subject']),
            'everyday_role' => $request['everyday_role'], 'ordinary_role_ids' => $request['ordinary_role_ids'], 'absent' => $absent];
    }

    public static function apply(array $request): array
    {
        return DB::transaction(function () use ($request): array {
            $key = 'kyquickstart-creation:' . $request['account']['setup_id'];
            $stored = Setting::query()->where('setting_key', $key)->lockForUpdate()->first();
            $intent = hash('sha256', json_encode([$request['installation_id'], Accounts::intentDigest($request['account']),
                $request['everyday_role'], $request['ordinary_role_ids']], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR));
            if ($request['action'] === 'check' && !$stored) {
                throw new RuntimeException('Creation receipt required');
            }
            $account = Accounts::apply($request['account'], (bool) $stored);
            $created = $account['created'];
            $roles = self::roles($request, $account['user_id'], $created ? 'initialize' : 'check');
            if ($stored) {
                $receipt = json_decode($stored->value, true, 8, JSON_THROW_ON_ERROR);
                $audit = Setting::query()->where('setting_key', $key . ':audit')->first();
                if ($created || !is_array($receipt) || ($receipt['contract'] ?? '') !== self::CONTRACT ||
                    ($receipt['intent_digest'] ?? '') !== $intent || ($receipt['user_id'] ?? null) !== $account['user_id'] ||
                    ($receipt['installation_id'] ?? '') !== $request['installation_id'] || !$audit || $audit->value !== $stored->value) {
                    throw new RuntimeException('Creation ownership or audit conflict');
                }
            } else {
                // An ordinary setup receipt by itself never authorizes adoption into this lane.
                if (!$created) { throw new RuntimeException('New owned creation required'); }
                Roles::initializeCreatedOrdinary($roles);
            }
            $profile = array_diff_key($roles, array_flip(['everyday_role', 'ordinary_role_ids', 'administrator']));
            $profile['contract'] = Profiles::CONTRACT;
            $profile['name'] = $account['name']; $profile['email'] = $account['email'];
            $directory = Directory::apply(['contract' => Directory::CONTRACT, 'profile' => $profile, 'roles' => $roles]);
            if (!$stored) {
                $value = json_encode(['contract' => self::CONTRACT, 'installation_id' => $request['installation_id'],
                    'intent_digest' => $intent, 'user_id' => $account['user_id']], JSON_THROW_ON_ERROR);
                foreach ([$key, $key . ':audit'] as $receiptKey) {
                    $row = new Setting(); $row->incrementing = false; $row->setting_key = $receiptKey;
                    $row->type = 'string'; $row->value = $value; $row->save();
                }
            }
            $account = Accounts::apply(array_replace($request['account'], ['action' => 'check']), (bool) $stored);
            $account['created'] = $created;
            return ['contract' => self::CONTRACT, 'module_version' => Revocations::VERSION,
                'installation_id' => $request['installation_id'], 'intent_digest' => $intent,
                'account' => $account, 'directory' => $directory];
        }, 3);
    }
}

Theme::registerCommand(new class extends Command {
    protected $signature = 'kyquickstart:create {--request= : Canonical private JSON request file}';
    protected $description = 'Create or inspect an owned ordinary account and its directory receipts atomically';
    public function handle(): int
    {
        try {
            $result = Creation::apply(Creation::validate(Revocations::readPrivateRequest((string) $this->option('request'))));
            Log::notice('kyquickstart ordinary creation', ['success' => true, 'user_id' => $result['account']['user_id'],
                'created' => $result['account']['created']]);
            $this->line(json_encode($result, JSON_THROW_ON_ERROR)); return 0;
        } catch (Throwable $error) {
            Log::notice('kyquickstart ordinary creation', ['success' => false]);
            $this->error('Creation request rejected'); return 1;
        }
    }
});
