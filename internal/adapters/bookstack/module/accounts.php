<?php
// SPDX-License-Identifier: MIT

namespace KyQuickStart\BookStack;

use BookStack\Facades\Theme;
use BookStack\Access\RegistrationService;
use BookStack\Access\Oidc\OidcService;
use BookStack\Access\Oidc\OidcException;
use BookStack\Exceptions\UserRegistrationException;
use BookStack\Permissions\Permission;
use BookStack\Theming\ThemeEvents;
use BookStack\Settings\Setting;
use BookStack\Users\Models\User;
use BookStack\Users\UserRepo;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use RuntimeException;
use Throwable;

final class Accounts
{
    public const CONTRACT = 'ky.bookstack-account/1';

    public static function request(string $path): array
    {
        return self::validate(Revocations::readPrivateRequest($path));
    }

    public static function validate(array $data): array
    {
        $keys = array_keys($data);
        sort($keys);
        if ($keys !== ['action', 'administrator', 'client_id', 'contract', 'email', 'issuer', 'name', 'setup_id', 'subject'] ||
            $data['contract'] !== self::CONTRACT || !in_array($data['action'], ['create', 'check', 'probe'], true) ||
            !is_string($data['setup_id']) || !preg_match('/^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/D', $data['setup_id']) ||
            !is_bool($data['administrator']) || !is_string($data['name']) || strlen($data['name']) < 1 || strlen($data['name']) > 100 ||
            preg_match('/[\x00-\x1f\x7f]/', $data['name']) || !is_string($data['email']) || strlen($data['email']) > 254 ||
            !filter_var($data['email'], FILTER_VALIDATE_EMAIL) || !is_string($data['subject']) || strlen($data['subject']) < 1 ||
            strlen($data['subject']) > 255 || preg_match('/[\x00-\x20\x7f]/', $data['subject']) ||
            !is_string($data['issuer']) || strlen($data['issuer']) > 2048 || !is_string($data['client_id']) ||
            strlen($data['client_id']) < 1 || strlen($data['client_id']) > 255 || preg_match('/[\x00-\x20\x7f]/', $data['client_id'])) {
            throw new RuntimeException('Invalid account request');
        }
        $url = parse_url($data['issuer']);
        if (!$url || ($url['scheme'] ?? '') !== 'https' || empty($url['host']) || isset($url['user']) || isset($url['pass']) ||
            isset($url['query']) || isset($url['fragment']) || preg_match('/[\x00-\x20\x7f\\\\]/', $data['issuer']) ||
            $data['issuer'] !== config('oidc.issuer') || $data['client_id'] !== config('oidc.client_id') ||
            config('oidc.external_id_claim') !== 'sub' || !config('oidc.user_to_groups') ||
            config('oidc.groups_claim') !== 'roles' || !config('oidc.remove_from_groups') ||
            config('oidc.fetch_avatar') || config('oidc.dump_user_details') || config('services.avatar_url') !== false) {
            throw new RuntimeException('Configured OIDC account source required');
        }
        return $data;
    }

    public static function intentDigest(array $request): string
    {
        return hash('sha256', json_encode([
            $request['setup_id'], $request['issuer'], $request['client_id'], $request['subject'],
            $request['name'], $request['email'], $request['administrator'],
        ], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR));
    }

    private static function sourceDigest(): string
    {
        return hash('sha256', (string) config('oidc.issuer') . "\0" . (string) config('oidc.client_id'));
    }

    private static function assignmentKey(string $subject): string
    {
        return 'kyquickstart-assignment:' . hash('sha256', (string) config('oidc.issuer') . "\0" . $subject);
    }

    public static function ownedUser(string $subject): User
    {
        $stored = Setting::query()->where('setting_key', self::assignmentKey($subject))->first();
        $receipt = $stored ? json_decode($stored->value, true) : null;
        if (!is_array($receipt) || ($receipt['source_digest'] ?? '') !== self::sourceDigest() ||
            !is_int($receipt['user_id'] ?? null) || !is_string($receipt['setup_id'] ?? null)) {
            throw new UserRegistrationException('Account is not assigned', '/login');
        }
        $primary = Setting::query()->where('setting_key', 'kyquickstart-account:' . $receipt['setup_id'])->first();
        $ownership = $primary ? json_decode($primary->value, true) : null;
        if (!is_array($ownership) || ($ownership['contract'] ?? '') !== self::CONTRACT ||
            ($ownership['user_id'] ?? null) !== $receipt['user_id']) {
            throw new UserRegistrationException('Account is not assigned', '/login');
        }
        $user = User::query()->find($receipt['user_id']);
        if (!$user || $user->isGuest() || $user->external_auth_id !== $subject) {
            throw new UserRegistrationException('Account is not assigned', '/login');
        }
        return $user;
    }

    public static function assignedUser(string $subject): User
    {
        $user = self::ownedUser($subject);
        if (Revocations::denied($user)) {
            throw new UserRegistrationException('Account is not assigned', '/login');
        }
        return $user;
    }

    public static function recordLogin(string $method, User $user): void
    {
        if ($method !== 'oidc') {
            return;
        }
        // This event follows native signature/audience/issuer verification and
        // role synchronization. Never use OIDC_ID_TOKEN_PRE_VALIDATE for authority.
        $proof = json_encode(['source_digest' => self::sourceDigest(), 'user_id' => $user->id,
            'subject_digest' => hash('sha256', $user->external_auth_id),
            'administrator' => $user->hasSystemRole('admin')], JSON_THROW_ON_ERROR);
        Setting::query()->upsert([['setting_key' => 'kyquickstart-oidc-login:' . $user->id,
            'value' => $proof, 'type' => 'string']], ['setting_key'], ['value', 'type', 'updated_at']);
    }

    public static function verifiedLogin(User $user, bool $admin): bool
    {
        $stored = Setting::query()->where('setting_key', 'kyquickstart-oidc-login:' . $user->id)->first();
        if (!$stored) {
            return false;
        }
        $proof = json_decode($stored->value, true);
        return is_array($proof) && ($proof['source_digest'] ?? '') === self::sourceDigest() &&
            ($proof['user_id'] ?? null) === $user->id &&
            ($proof['subject_digest'] ?? '') === hash('sha256', $user->external_auth_id) &&
            ($proof['administrator'] ?? null) === $admin;
    }

    public static function apply(array $request, bool $projectedAuthority = false): array
    {
        if ($request['action'] === 'probe') {
            return ['contract' => 'ky.bookstack-account-source/1', 'module_version' => Revocations::VERSION,
                'source_digest' => hash('sha256', $request['issuer'] . "\0" . $request['client_id'])];
        }
        return DB::transaction(function () use ($request, $projectedAuthority): array {
            $key = 'kyquickstart-account:' . $request['setup_id'];
            $stored = Setting::query()->where('setting_key', $key)->lockForUpdate()->first();
            $created = false;
            $intent = self::intentDigest($request);
            $assignmentKey = self::assignmentKey($request['subject']);
            $assignment = Setting::query()->where('setting_key', $assignmentKey)->lockForUpdate()->first();
            if ($stored) {
                $receipt = json_decode($stored->value, true, 8, JSON_THROW_ON_ERROR);
                if (!is_array($receipt) || ($receipt['contract'] ?? '') !== self::CONTRACT ||
                    ($receipt['intent_digest'] ?? '') !== $intent || !is_int($receipt['user_id'] ?? null)) {
                    throw new RuntimeException('Account ownership receipt conflict');
                }
                $user = User::query()->whereKey($receipt['user_id'])->lockForUpdate()->first();
                if (!$user || $user->isGuest() || $user->external_auth_id !== $request['subject']) {
                    throw new RuntimeException('Retained account binding conflict');
                }
                $assigned = $assignment ? json_decode($assignment->value, true) : null;
                if (!is_array($assigned) || ($assigned['setup_id'] ?? '') !== $request['setup_id'] ||
                    ($assigned['user_id'] ?? null) !== $user->id || ($assigned['source_digest'] ?? '') !== self::sourceDigest()) {
                    throw new RuntimeException('Account assignment receipt conflict');
                }
            } else {
                if ($request['action'] === 'check') {
                    throw new RuntimeException('Account has not been provisioned');
                }
                // Collation-normalized collisions also refuse creation. Names and email
                // never authorize adoption, even when the external subject appears to match.
                if ($assignment || Revocations::subjectDenied($request['subject']) || User::query()->where('email', $request['email'])
                    ->orWhere('external_auth_id', $request['subject'])->exists()) {
                    throw new RuntimeException('Unowned or revoked account collision');
                }
                $user = app(UserRepo::class)->create([
                    'name' => $request['name'], 'email' => $request['email'],
                    'external_auth_id' => $request['subject'], 'roles' => [],
                ], false);
                if ($user->hasSystemRole('admin') || Revocations::denied($user)) {
                    throw new RuntimeException('Unexpected initial account authority');
                }
                $stored = new Setting();
                $stored->incrementing = false;
                $stored->setting_key = $key;
                $stored->type = 'string';
                $stored->value = json_encode(['contract' => self::CONTRACT, 'user_id' => $user->id,
                    'intent_digest' => $intent], JSON_THROW_ON_ERROR);
                $stored->save();
                $assignment = new Setting();
                $assignment->incrementing = false;
                $assignment->setting_key = $assignmentKey;
                $assignment->type = 'string';
                $assignment->value = json_encode(['setup_id' => $request['setup_id'], 'user_id' => $user->id,
                    'source_digest' => self::sourceDigest()], JSON_THROW_ON_ERROR);
                $assignment->save();
                $created = true;
            }
            $admin = $user->hasSystemRole('admin');
            $verified = self::verifiedLogin($user, $admin);
            if (!$projectedAuthority && !$request['administrator'] && ($admin || $user->can(Permission::UsersManage) ||
                $user->can(Permission::UserRolesManage) || $user->can(Permission::SettingsManage))) {
                throw new RuntimeException('Everyday account has administrator authority');
            }
            return [
                'contract' => 'ky.bookstack-account-report/2', 'module_version' => Revocations::VERSION,
                'name' => $user->name, 'email' => $user->email,
                'setup_id' => $request['setup_id'], 'intent_digest' => $intent,
                'user_id' => $user->id, 'binding_digest' => hash('sha256', $request['issuer'] . "\0" . $request['subject']),
                'created' => $created, 'disabled' => Revocations::denied($user), 'administrator' => $admin,
                'oidc_verified' => $verified,
                'activation_pending' => config('auth.method') !== 'oidc',
                'admin_grant_pending' => $request['administrator'] && (!$admin || !$verified),
            ];
        }, 3);
    }
}

// Native OIDC calls this after validating the token and before group synchronization.
// Refuse unassigned or revoked subjects before they can create users or change roles.
final class AssignedRegistrationService extends RegistrationService
{
    public function findOrRegister(string $name, string $email, string $externalId): User
    {
        return config('auth.method') === 'oidc' ? Accounts::assignedUser($externalId) :
            parent::findOrRegister($name, $email, $externalId);
    }
}

final class AssignedOidcService extends OidcService
{
    private bool $managedRoles = false;

    protected function processAccessTokenCallback(\BookStack\Access\Oidc\OidcAccessToken $accessToken, \BookStack\Access\Oidc\OidcProviderSettings $settings): User
    {
        $this->managedRoles = false;
        return DB::transaction(fn (): User => parent::processAccessTokenCallback($accessToken, $settings), 3);
    }

    protected function getUserDetailsFromToken(\BookStack\Access\Oidc\OidcIdToken $idToken, \BookStack\Access\Oidc\OidcAccessToken $accessToken, \BookStack\Access\Oidc\OidcProviderSettings $settings): \BookStack\Access\Oidc\OidcUserDetails
    {
        $details = parent::getUserDetailsFromToken($idToken, $accessToken, $settings);
        try {
            $user = Accounts::assignedUser((string) $details->externalId);
            $this->managedRoles = Roles::verifyLogin($user, $idToken->getClaim('roles'));
        } catch (Throwable $error) {
            throw new OidcException('Owned identity authority unavailable');
        }
        return $details;
    }

    protected function shouldSyncGroups(): bool
    {
        return !$this->managedRoles && parent::shouldSyncGroups();
    }

    public function processAuthorizeResponse(?string $authorizationCode): User
    {
        // The native controller has already consumed and checked callback state.
        // A provider denial has no code; do not pass it to the token exchange library.
        if ($authorizationCode === null || $authorizationCode === '') {
            session()->forget('oidc_pkce_code');
            throw new OidcException('Identity provider did not authorize access');
        }
        return parent::processAuthorizeResponse($authorizationCode);
    }
}

Theme::listen(ThemeEvents::APP_BOOT, function ($app): void {
    $app->bind(RegistrationService::class, AssignedRegistrationService::class);
    $app->bind(OidcService::class, AssignedOidcService::class);
});

Theme::listen(ThemeEvents::AUTH_LOGIN, [Accounts::class, 'recordLogin']);

Theme::registerCommand(new class extends Command {
    protected $signature = 'kyquickstart:account {--request= : Canonical private JSON request file}';
    protected $description = 'Provision or inspect an exact receipt-owned OIDC account';

    public function handle(): int
    {
        try {
            $request = Accounts::request((string) $this->option('request'));
            $result = Accounts::apply($request);
            Log::notice('kyquickstart account setup', ['setup_id' => $request['setup_id'],
                'user_id' => $result['user_id'] ?? null, 'created' => $result['created'] ?? false, 'success' => true]);
            $this->line(json_encode($result, JSON_THROW_ON_ERROR));
            return 0;
        } catch (Throwable $error) {
            Log::notice('kyquickstart account setup', ['success' => false]);
            $this->error('Account setup rejected');
            return 1;
        }
    }
});
