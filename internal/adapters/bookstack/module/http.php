<?php
// SPDX-License-Identifier: MIT

namespace KyQuickStart\BookStack;

use BookStack\Facades\Theme;
use BookStack\Http\Middleware\TrimStrings;
use BookStack\Theming\ThemeEvents;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Facades\Route;
use RuntimeException;
use Throwable;

final class BridgeEndpoint
{
    public const PATH = 'api/kyquickstart/offboard';

    public static function credential(string $environment): array
    {
        $path = (string) env($environment, '');
        Revocations::outsidePublic($path);
        $credential = Revocations::readPrivateRequest($path);
        if (array_keys($credential) !== ['token'] || !is_string($credential['token']) ||
            !preg_match('/^[a-f0-9]{64}$/D', $credential['token'])) {
            throw new RuntimeException('Bridge credential unavailable');
        }
        return $credential;
    }

    public static function body(Request $request, array $credential, int &$status): array
    {
        $status = 401;
        if (!$request->isSecure() || !preg_match('/^Bearer ([a-f0-9]{64})$/D', (string) $request->header('Authorization'), $match) ||
            !hash_equals($credential['token'], $match[1])) {
            throw new RuntimeException('Bridge authentication refused');
        }
        $status = 400;
        if (strtolower(explode(';', (string) $request->header('Content-Type'))[0]) !== 'application/json') {
            throw new RuntimeException('JSON request required');
        }
        // The route skips native string trimming/JSON pre-parsing. Bound the raw
        // stream before decoding, and retain byte-exact issuer/subject values.
        $stream = $request->getContent(true);
        $body = stream_get_contents($stream, 4097);
        if ($body === false || strlen($body) > 4096) {
            throw new RuntimeException('Bounded request required');
        }
        $data = json_decode($body, true, 8, JSON_THROW_ON_ERROR);
        if (!is_array($data)) {
            throw new RuntimeException('Invalid request');
        }
        return $data;
    }

    public static function handle(Request $request)
    {
        $status = 503;
        try {
            $data = self::body($request, self::credential('KYQ_BOOKSTACK_BRIDGE_TOKEN_FILE'), $status);
            $binding = Revocations::validateRequest($data);
            $owned = Accounts::ownedUser($binding['subject']);
            if ($owned->id !== $binding['user_id']) {
                throw new RuntimeException('Owned identity required');
            }
            $result = Revocations::apply($binding);
            // Native mutation has returned; a reporting failure cannot prove refusal.
            $status = 503;
            Log::notice('kyquickstart bridge offboarding', ['success' => true, 'action' => $binding['action'],
                'user_id' => $result['user_id'], 'binding_digest' => $result['binding_digest']]);
            return response()->json($result)->header('Cache-Control', 'no-store');
        } catch (Throwable $error) {
            // A database/commit error has no provable outcome at the HTTP boundary.
            if ($error instanceof \PDOException) { $status = 503; }
            Log::notice('kyquickstart bridge offboarding', ['success' => false]);
            return response()->json(['error' => 'Bridge request refused'], $status)->header('Cache-Control', 'no-store');
        }
    }
}

final class DirectoryEndpoint
{
    public const PATH = 'api/kyquickstart/directory';

    public static function handle(Request $request)
    {
        $status = 503;
        try {
            $credential = BridgeEndpoint::credential('KYQ_BOOKSTACK_DIRECTORY_TOKEN_FILE');
            $offboarding = BridgeEndpoint::credential('KYQ_BOOKSTACK_BRIDGE_TOKEN_FILE');
            if (hash_equals($offboarding['token'], $credential['token'])) {
                throw new RuntimeException('Separate directory authority required');
            }
            $data = Directory::validate(BridgeEndpoint::body($request, $credential, $status));
            if (!in_array($data['profile']['action'], ['check', 'apply'], true)) {
                throw new RuntimeException('Native initialization required');
            }
            $result = Directory::apply($data);
            // Native mutation has returned; a reporting failure cannot prove refusal.
            $status = 503;
            Log::notice('kyquickstart directory endpoint', ['success' => true,
                'user_id' => $result['profile']['user_id'], 'profile_revision' => $result['profile']['revision'],
                'role_revision' => $result['roles']['revision']]);
            return response()->json($result)->header('Cache-Control', 'no-store');
        } catch (Throwable $error) {
            // A database/commit error has no provable outcome at the HTTP boundary.
            if ($error instanceof \PDOException) { $status = 503; }
            Log::notice('kyquickstart directory endpoint', ['success' => false]);
            return response()->json(['error' => 'Directory request refused'], $status)->header('Cache-Control', 'no-store');
        }
    }
}

final class CreationEndpoint
{
    public const PATH = 'api/kyquickstart/creation';

    public static function handle(Request $request)
    {
        $status = 503;
        try {
            $credential = BridgeEndpoint::credential('KYQ_BOOKSTACK_CREATION_TOKEN_FILE');
            foreach (['KYQ_BOOKSTACK_BRIDGE_TOKEN_FILE', 'KYQ_BOOKSTACK_DIRECTORY_TOKEN_FILE'] as $environment) {
                if (hash_equals($credential['token'], BridgeEndpoint::credential($environment)['token'])) {
                    throw new RuntimeException('Separate creation authority required');
                }
            }
            $path = (string) env('KYQ_BOOKSTACK_CREATION_POLICY_FILE', '');
            Revocations::outsidePublic($path);
            $policy = Revocations::readPrivateRequest($path);
            $keys = array_keys($policy); sort($keys);
            if ($keys !== ['client_id', 'everyday_role', 'installation_id', 'issuer', 'ordinary_role_ids']) {
                throw new RuntimeException('Creation policy required');
            }
            $data = BridgeEndpoint::body($request, $credential, $status);
            $scope = ($data['contract'] ?? '') === Creation::SCOPE_CONTRACT;
            $probe = !$scope && ($data['action'] ?? '') === 'probe';
            if ($probe) {
                if (($data['account']['action'] ?? '') !== 'probe') { throw new RuntimeException('One probe action required'); }
                $data['action'] = 'create'; $data['account']['action'] = 'create';
            }
            $data = $scope ? Creation::validateScope($data) : Creation::validate($data);
            foreach (['installation_id', 'everyday_role', 'ordinary_role_ids'] as $field) {
                if ($data[$field] !== $policy[$field]) { throw new RuntimeException('Pinned creation policy required'); }
            }
            $source = $scope ? $data : $data['account'];
            foreach (['issuer', 'client_id'] as $field) {
                if ($source[$field] !== $policy[$field]) { throw new RuntimeException('Pinned creation source required'); }
            }
            $result = $scope ? Creation::inspectScope($data) : ($probe ? Creation::absence($data) : Creation::apply($data));
            // Native mutation has returned; a reporting failure cannot prove refusal.
            $status = 503;
            Log::notice('kyquickstart creation endpoint', ['success' => true, 'action' => $probe ? 'probe' : $data['action']]);
            return response()->json($result)->header('Cache-Control', 'no-store');
        } catch (Throwable $error) {
            // A database/commit error has no provable outcome at the HTTP boundary.
            if ($error instanceof \PDOException) { $status = 503; }
            Log::notice('kyquickstart creation endpoint', ['success' => false]);
            return response()->json(['error' => 'Creation request refused'], $status)->header('Cache-Control', 'no-store');
        }
    }
}

Theme::listen(ThemeEvents::APP_BOOT, function (): void {
    TrimStrings::skipWhen(fn (Request $request): bool => in_array($request->path(), [BridgeEndpoint::PATH, DirectoryEndpoint::PATH, CreationEndpoint::PATH], true));
    // No web/api group: session cookies, native API tokens and CSRF grants provide
    // no authority here. The endpoint authenticates only its separate scoped token.
    Route::post('/' . BridgeEndpoint::PATH, [BridgeEndpoint::class, 'handle']);
    Route::post('/' . DirectoryEndpoint::PATH, [DirectoryEndpoint::class, 'handle']);
    Route::post('/' . CreationEndpoint::PATH, [CreationEndpoint::class, 'handle']);
});
