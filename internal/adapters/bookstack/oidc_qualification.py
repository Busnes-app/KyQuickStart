#!/usr/bin/env python3
"""Signed native OIDC qualification against nonce-owned disposable services."""
import base64
import contextlib
import http.client
import http.cookiejar
import json
import os
import pathlib
import secrets
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from qualification import Fields, Web, cleanup, denied, diagnose, docker, offboard, pinned
from native_identity import NativeIdentity
from native_producer import NativeProducer


class MappedHTTPS(urllib.request.HTTPSHandler):
    def __init__(self, context, issuer_host, port):
        super().__init__(context=context)
        self.issuer_host, self.port = issuer_host, port

    def https_open(self, request):
        issuer_host, port = self.issuer_host, self.port

        class Connection(http.client.HTTPSConnection):
            def connect(self):
                if self.host == issuer_host:
                    raw = socket.create_connection(('127.0.0.1', port), self.timeout)
                    self.sock = self._context.wrap_socket(raw, server_hostname=self.host)
                else:
                    if self.host != '127.0.0.1' or self.port != port:
                        raise AssertionError('fixture escaped owned TLS origins')
                    super().connect()

        return self.do_open(Connection, request, context=self._context)


class OIDCWeb(Web):
    def __init__(self, origin, issuer, ca, port, provider=None, exchanges=None):
        self.origin, self.issuer = origin, issuer
        self.provider = provider
        self.expected_exchanges = exchanges
        context = ssl.create_default_context(cafile=str(ca))
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
            MappedHTTPS(context, urllib.parse.urlsplit(issuer).hostname, port),
        )

    def oidc(self, actor):
        if self.expected_exchanges is not None:
            self.expected_exchanges[0] += 1
        if self.provider:
            self.provider.login(self, actor)
        else:
            with self.opener.open(self.issuer + '/actor?who=' + actor, timeout=5) as response:
                response.read(1024)
        token = Fields(self.request('/login')).named['_token']
        return self.request('/oidc/login', [('_token', token)])

    def allowed_json(self, path):
        request = urllib.request.Request(self.origin + path, headers={'Accept': 'application/json'})
        with self.opener.open(request, timeout=5) as response:
            raw = response.read((1 << 20) + 1)
            if response.status != 200 or response.url != self.origin + path or len(raw) > 1 << 20:
                raise AssertionError('native administrator API access not verified')
            return json.loads(raw)

    def denied_json(self, path):
        request = urllib.request.Request(self.origin + path, headers={'Accept': 'application/json'})
        try:
            with self.opener.open(request, timeout=5):
                raise AssertionError('native JSON boundary still authorizes ' + path)
        except urllib.error.HTTPError as error:
            if error.code not in (401, 403):
                raise


def account(app, request, action='check'):
    encoded = base64.b64encode(json.dumps({**request, 'action': action}).encode()).decode()
    path = '/config/kyq-fixture/account-' + secrets.token_hex(8) + '.json'
    docker('exec', '-u', 'abc', app, 'sh', '-c',
           "umask 077; mkdir -p /config/kyq-fixture; printf '%s' '" + encoded + "' | base64 -d > " + path)
    try:
        result = subprocess.run(['docker', 'exec', '-u', 'abc', '-w', '/app/www', app,
                                 'php', 'artisan', 'kyquickstart:account', '--request=' + path],
                                capture_output=True, timeout=30)
        if result.returncode:
            raise AssertionError('native account command failed')
        return json.loads(result.stdout)
    finally:
        cleanup('exec', '-u', 'abc', app, 'rm', '-f', path)


def bridge_request(web, token, binding, expected=None, endpoint='offboard'):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    request = urllib.request.Request(web.origin + '/api/kyquickstart/' + endpoint, json.dumps(binding).encode(), headers)
    try:
        with web.opener.open(request, timeout=5) as response:
            result = json.loads(response.read(16385))
    except urllib.error.HTTPError as error:
        if error.code != expected or json.loads(error.read(4096)) != {'error': {'directory': 'Directory request refused', 'creation': 'Creation request refused', 'offboard': 'Bridge request refused'}[endpoint]}:
            raise AssertionError('native bridge rejection status/body mismatch')
        return
    if expected is not None:
        raise AssertionError('native bridge accepted an unauthorized request')
    return result


def main():
    app_image = os.environ.get('KYQ_TEST_BOOKSTACK_IMAGE', '')
    db_image = os.environ.get('KYQ_TEST_BOOKSTACK_DB_IMAGE', '')
    if not pinned(app_image) or not pinned(db_image):
        raise AssertionError('set digest-pinned fixture images')
    producer_mode = os.environ.get('KYQ_TEST_BOOKSTACK_PRODUCER') == '1'
    if producer_mode and (not os.environ.get('KYQ_TEST_BOOKSTACK_IDENTITY_BINARY') or os.environ.get('KYQ_TEST_BOOKSTACK_DIRECTORY') == '1'):
        raise AssertionError('producer requires actual identity and excludes controlled directory mode')
    create_mode = os.environ.get('KYQ_TEST_BOOKSTACK_CREATE') == '1'
    if create_mode and not os.environ.get('KYQ_TEST_BOOKSTACK_IDENTITY_BINARY'):
        raise AssertionError('native creation fixture requires actual identity')
    if os.environ.get('KYQ_TEST_BOOKSTACK_CREATE_HTTP') == '1' and not create_mode:
        raise AssertionError('HTTPS creation requires native creation mode')
    suffix = secrets.token_hex(6)
    network, db, app, gateway = ['kyq-bookstack-' + suffix + ending for ending in ('-net', '-db', '-app', '-gateway')]
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    origin, issuer = 'https://127.0.0.1:' + str(port), 'https://' + gateway + ':8443/fixture-idp'
    identity_binary = os.environ.get('KYQ_TEST_BOOKSTACK_IDENTITY_BINARY', '')
    with contextlib.ExitStack() as owned:
        directory = pathlib.Path(owned.enter_context(tempfile.TemporaryDirectory(prefix='kyq-bookstack-oidc-')))
        directory.chmod(0o755)  # Public fixture CA and executable; private keys remain in RAM.
        subprocess.run(['go', 'build', '-o', str(directory / 'gateway'), './internal/adapters/bookstack/testgateway'],
                       check=True, env={**os.environ, 'CGO_ENABLED': '0'}, timeout=120)
        (directory / 'php').mkdir()
        (directory / 'php' / 'fixture.ini').write_text('openssl.cafile=/fixture/ca.pem\ncurl.cainfo=/fixture/ca.pem\n')
        producer_binary = directory / 'producer.test'
        if producer_mode:
            subprocess.run(['go', 'test', '-race', '-p', '1', '-c', '-o', str(producer_binary), './internal/offboard'], check=True, capture_output=True, timeout=120)
        docker('network', 'create', network)
        owned.callback(cleanup, 'network', 'rm', network)
        producer = NativeProducer(owned, directory / 'producer', network, producer_binary) if producer_mode else None
        identity_name = 'kyq-bookstack-' + suffix + '-identity'
        identity_args = ['-e', 'FIXTURE_IDENTITY_BACKEND=http://' + identity_name + ':5867'] if identity_binary else []
        docker('run', '-d', '--name', gateway, '--network', network, '--memory', '256m', '--memory-swap', '256m', '--cpus', '1', '--cap-drop', 'ALL',
               '--security-opt', 'no-new-privileges', '--user', str(os.getuid()) + ':' + str(os.getgid()),
               '--entrypoint', '/fixture/gateway', '-v', str(directory) + ':/fixture',
               '-p', '127.0.0.1:' + str(port) + ':8443', '-e', 'FIXTURE_ISSUER=' + issuer,
               '-e', 'FIXTURE_ORIGIN=' + origin, '-e', 'FIXTURE_BACKEND=http://' + app + ':80', *identity_args, app_image)
        owned.callback(cleanup, 'rm', '-fv', gateway)
        ca = directory / 'ca.pem'
        deadline = time.monotonic() + 10
        while not ca.exists():
            if time.monotonic() >= deadline:
                raise AssertionError('TLS fixture startup failed')
            time.sleep(0.1)
        proxy_ip = json.loads(docker('inspect', '--format', '{{json .NetworkSettings.Networks}}', gateway))[network]['IPAddress']
        provider = None
        if identity_binary:
            data = pathlib.Path(owned.enter_context(tempfile.TemporaryDirectory(prefix='kyq-bookstack-identity-')))
            provider = NativeIdentity(owned, data, identity_binary, app_image, identity_name, network, issuer, proxy_ip, producer, ca)
            provider.provision(origin)
        docker('run', '-d', '--name', db, '--network', network, '--memory', '768m', '--memory-swap', '768m', '--cpus', '1', '--cap-drop', 'NET_RAW',
               '-e', 'MARIADB_ROOT_PASSWORD=disposable-fixture-root-password', '-e', 'MARIADB_DATABASE=bookstack',
               '-e', 'MARIADB_USER=bookstack', '-e', 'MARIADB_PASSWORD=disposable-fixture-db-password', db_image)
        owned.callback(cleanup, 'rm', '-fv', db)
        docker('run', '-d', '--name', app, '--network', network, '--memory', '1g', '--memory-swap', '1g', '--cpus', '1', '--cap-drop', 'NET_RAW',
               '--label', 'kyq.fixture=bookstack', '--label', 'kyq.fixture-owner=' + suffix,
               '-v', str(directory) + ':/fixture:ro', '-e', 'PHP_INI_SCAN_DIR=:/fixture/php',
               '-e', 'APP_URL=' + origin, '-e', 'APP_PROXIES=' + proxy_ip,
               '-e', 'APP_KEY=base64:' + base64.b64encode(secrets.token_bytes(32)).decode(),
               '-e', 'DB_HOST=' + db, '-e', 'DB_DATABASE=bookstack', '-e', 'DB_USERNAME=bookstack',
               '-e', 'DB_PASSWORD=disposable-fixture-db-password', '-e', 'AUTH_METHOD=oidc',
               '-e', 'OIDC_ISSUER=' + issuer, '-e', 'OIDC_ISSUER_DISCOVER=true',
               '-e', 'OIDC_CLIENT_ID=fixture-client', '-e', 'OIDC_CLIENT_SECRET=disposable-fixture-client-secret',
               '-e', 'OIDC_USER_TO_GROUPS=true', '-e', 'OIDC_GROUPS_CLAIM=roles', '-e', 'OIDC_REMOVE_FROM_GROUPS=true',
               '-e', 'AVATAR_URL=false', '-e', 'KYQ_BOOKSTACK_REVOCATION_DIR=/config/kyquickstart-revocations',
               '-e', 'KYQ_BOOKSTACK_BRIDGE_TOKEN_FILE=/config/kyquickstart-bridge/token.json',
               '-e', 'KYQ_BOOKSTACK_DIRECTORY_TOKEN_FILE=/config/kyquickstart-bridge/directory.json',
               '-e', 'KYQ_BOOKSTACK_CREATION_TOKEN_FILE=/config/kyquickstart-bridge/creation.json',
               '-e', 'KYQ_BOOKSTACK_CREATION_POLICY_FILE=/config/kyquickstart-bridge/creation-policy.json',
               '-e', 'APP_THEME=kyquickstart', app_image)
        owned.callback(cleanup, 'rm', '-fv', app)
        owned.callback(diagnose, app)
        expected_exchanges = [0]
        web = lambda: OIDCWeb(origin, issuer, ca, port, provider, expected_exchanges)
        deadline = time.monotonic() + 120
        while True:
            try:
                web().request('/login')
                break
            except (OSError, urllib.error.URLError):
                if time.monotonic() >= deadline:
                    raise AssertionError('native OIDC startup timed out')
                time.sleep(0.25)
        docker('exec', '-u', 'abc', app, 'mkdir', '-m', '700', '/config/kyquickstart-revocations')
        docker('exec', app, 'mkdir', '-p', '/config/www/themes/kyquickstart/modules')
        docker('cp', str(pathlib.Path(__file__).resolve().parent / 'module'), app + ':/config/www/themes/kyquickstart/modules/offboarding')
        for name in ('functions.php', 'accounts.php', 'http.php', 'profiles.php', 'roles.php', 'directory.php', 'creation.php'):
            docker('exec', app, 'php', '-l', '/config/www/themes/kyquickstart/modules/offboarding/' + name)
        bridge_token = secrets.token_hex(32)
        staged = subprocess.run(['docker', 'exec', '-i', '-u', 'abc', app, 'sh', '-c',
                                 'umask 077; mkdir -m 700 /config/kyquickstart-bridge; cat > /config/kyquickstart-bridge/token.json'],
                                input=json.dumps({'token': bridge_token}).encode(), capture_output=True, timeout=10)
        if staged.returncode:
            raise AssertionError('native scoped credential staging failed')
        directory_token = secrets.token_hex(32)
        def stage_directory_token(token):
            staged = subprocess.run(['docker', 'exec', '-i', '-u', 'abc', app, 'sh', '-c',
                                     'umask 077; cat > /config/kyquickstart-bridge/directory.json'],
                                    input=json.dumps({'token': token}).encode(), capture_output=True, timeout=10)
            if staged.returncode:
                raise AssertionError('native directory credential staging failed')
        stage_directory_token(directory_token)
        common = {'contract': 'ky.bookstack-account/1', 'issuer': issuer, 'client_id': 'fixture-client'}
        owner_spec = {**common, 'setup_id': '2c676879-9c71-4913-b8a7-995521d9ed20',
                      'subject': 'owner-subject', 'name': 'Owner', 'email': 'owner@example.test', 'administrator': True}
        daily_spec = {**common, 'setup_id': '43f864f0-991c-43bf-8b94-d93c2916a98f',
                      'subject': 'daily-subject', 'name': 'Everyday', 'email': 'daily@example.test', 'administrator': False}
        if provider:
            owner_spec['subject'] = provider.actors['owner']
            daily_spec['subject'] = provider.actors['daily']
        initial_owner = account(app, owner_spec, 'create')
        initial_daily = account(app, daily_spec, 'create')
        print('Native OIDC accounts created without seeded roles.')
        if initial_owner['administrator'] or initial_owner['oidc_verified'] or not initial_owner['admin_grant_pending']:
            raise AssertionError('owner authority was seeded before signed OIDC')
        if not provider:
            web().oidc('bad-signature')
            print('Invalid-signature callback exercised.')
            if account(app, owner_spec) != {**initial_owner, 'created': False}:
                raise AssertionError('invalid signature changed owner authority')
        owner = web()
        owner.oidc('owner')
        print('Owner signed callback exercised.')
        granted = account(app, owner_spec)
        if not granted['administrator'] or not granted['oidc_verified'] or granted['admin_grant_pending'] or granted['activation_pending']:
            raise AssertionError('signed native OIDC did not prove owner grant')
        owner.request('/settings/users')
        admin_token = owner.token(granted['user_id'])
        api = web()
        unassigned = web()
        unassigned.oidc('unassigned')
        print('Unassigned-subject callback exercised.')
        users = json.loads(api.request('/api/users', token=admin_token))['data']
        if any(user['email'] == 'unassigned@example.test' for user in users):
            raise AssertionError('unassigned OIDC subject created an account')
        unassigned.denied_json('/api/books')
        json.loads(api.request('/api/roles', {'display_name': 'Fixture API editor', 'external_auth_id': 'editor',
                         'permissions': ['access-api', 'book-view-all', 'book-create-all']}, method='POST', token=admin_token))
        daily = web()
        daily.oidc('daily')
        checked_daily = account(app, daily_spec)
        if checked_daily['administrator'] or not checked_daily['oidc_verified']:
            raise AssertionError('everyday OIDC authority mismatch')
        daily.denied_json('/settings/users')
        profile = {'contract': 'ky.bookstack-profile/1', 'action': 'initialize',
                   'user_id': initial_daily['user_id'], 'setup_id': daily_spec['setup_id'],
                   'issuer': issuer, 'client_id': 'fixture-client', 'subject': daily_spec['subject'],
                   'name': checked_daily['name'], 'email': checked_daily['email'], 'expected_revision': 0}
        native_user = json.loads(api.request('/api/users/' + str(initial_daily['user_id']), token=admin_token))
        ordinary_ids = sorted(role['id'] for role in native_user['roles'])
        roles = {key: value for key, value in profile.items() if key not in ('name', 'email')}
        roles.update(contract='ky.bookstack-roles/1', everyday_role='editor',
                     ordinary_role_ids=ordinary_ids, administrator=False)
        directory = {'contract': 'ky.bookstack-directory/1', 'profile': profile, 'roles': roles}
        directory_uninitialized = {**directory, 'profile': {**profile, 'action': 'check'}, 'roles': {**roles, 'action': 'check'}}
        bridge_request(api, directory_token, directory_uninitialized, expected=400, endpoint='directory')
        offboard(app, {**directory, 'roles': {**roles, 'administrator': True}},
                 command='kyquickstart:directory', reject=True)
        offboard(app, {**profile, 'action': 'check'}, command='kyquickstart:profile', reject=True)
        offboard(app, {**roles, 'action': 'check'}, command='kyquickstart:roles', reject=True)
        # Every duplicated action/source field must agree before native mutation.
        for key, value in (('action', 'apply'), ('user_id', initial_daily['user_id'] + 1),
                           ('setup_id', owner_spec['setup_id']), ('subject', owner_spec['subject'])):
            offboard(app, {**directory, 'roles': {**roles, key: value}},
                     command='kyquickstart:directory', reject=True)
        initialized_directory = offboard(app, directory, command='kyquickstart:directory')
        initialized = initialized_directory['profile']
        if (initialized_directory['contract'] != 'ky.bookstack-directory/1' or
                initialized_directory['roles']['revision'] != 0 or initialized_directory['roles']['administrator']):
            raise AssertionError('combined native directory initialization failed')
        print('PASS: combined initialization refuses mismatched binding and rolls back a failed second receipt.')
        directory_check = {**directory, 'profile': {**profile, 'action': 'check'}, 'roles': {**roles, 'action': 'check'}}
        checked = bridge_request(api, directory_token, directory_check, endpoint='directory')
        if checked != initialized_directory:
            raise AssertionError('HTTPS directory readback differs from native receipt')
        for actor, credential in ((owner, None), (api, bridge_token), (api, admin_token), (api, '0' * 64)):
            bridge_request(actor, credential, directory_check, expected=401, endpoint='directory')
        bridge_request(api, directory_token, directory, expected=400, endpoint='directory')
        bridge_request(api, directory_token, {**directory_check, 'profile': {**directory_check['profile'], 'subject': daily_spec['subject'] + ' '}},
                       expected=400, endpoint='directory')
        bridge_request(api, directory_token, {**directory_check, 'profile': {**directory_check['profile'], 'name': 'credential-must-not-reflect' * 300}},
                       expected=400, endpoint='directory')
        docker('exec', '-u', 'abc', app, 'chmod', '644', '/config/kyquickstart-bridge/directory.json')
        bridge_request(api, directory_token, directory_check, expected=503, endpoint='directory')
        docker('exec', '-u', 'abc', app, 'chmod', '600', '/config/kyquickstart-bridge/directory.json')
        stage_directory_token(bridge_token)
        bridge_request(api, bridge_token, directory_check, expected=503, endpoint='directory')
        stage_directory_token(directory_token)
        if bridge_request(api, directory_token, directory_check, endpoint='directory') != initialized_directory:
            raise AssertionError('refused HTTPS directory request changed receipts')
        print('PASS: HTTPS directory has distinct protected authority; cookie/native/offboarding credentials, initialization and malformed requests refuse.')

        projected_request = {**profile, 'action': 'apply', 'name': 'Directory everyday', 'email': 'directory-daily@example.test'}
        projected = offboard(app, projected_request, command='kyquickstart:profile')
        if initialized['revision'] != 0 or projected['revision'] != 1 or projected['disabled']:
            raise AssertionError('native OIDC profile projection failed')
        fresh = web()
        fresh.oidc('daily')
        fresh.request('/books')
        fresh.denied_json('/settings/users')
        retained_profile = offboard(app, {**profile, 'action': 'check'}, command='kyquickstart:profile')
        if retained_profile != projected:
            raise AssertionError('fresh signed OIDC changed owned projected profile')
        checked_daily = account(app, daily_spec)
        if (checked_daily['name'] != projected['name'] or checked_daily['email'] != projected['email'] or
                checked_daily['administrator'] or not checked_daily['oidc_verified']):
            raise AssertionError('projected everyday profile or native authority drifted')
        print('PASS: native profile projection survives fresh signed OIDC without changing everyday authority.')
        native_user = json.loads(api.request('/api/users/' + str(initial_daily['user_id']), token=admin_token))
        ordinary_ids = sorted(role['id'] for role in native_user['roles'])
        role_env = {**os.environ, 'KYQ_TEST_BOOKSTACK_OIDC_CONTAINER': app,
                    'KYQ_TEST_BOOKSTACK_USER_ID': str(initial_daily['user_id']),
                    'KYQ_TEST_BOOKSTACK_FIXTURE_OWNER': suffix,
                    'KYQ_TEST_BOOKSTACK_DAILY_SUBJECT': daily_spec['subject'],
                    'KYQ_TEST_BOOKSTACK_OWNER_SUBJECT': owner_spec['subject'],
                    'KYQ_TEST_BOOKSTACK_ORDINARY_ROLE_IDS': json.dumps(ordinary_ids)}
        def role_phase(phase):
            native = subprocess.run(['go', 'test', '-race', '-v', '-count=1', './internal/adapters/bookstack',
                                     '-run', '^TestNativeOIDCAccountVerification$'], capture_output=True, timeout=120,
                                    env={**role_env, 'KYQ_TEST_BOOKSTACK_ROLES': phase})
            print(native.stdout.decode().strip())
            if native.returncode or b'--- PASS: TestNativeOIDCAccountVerification' not in native.stdout or b'--- SKIP:' in native.stdout:
                raise AssertionError('native role phase failed or skipped: ' + phase)
        role_phase('grant')
        daily.allowed_json('/api/users')
        conflicting = web()
        conflicting.oidc('daily')
        conflicting.denied_json('/api/books')
        if provider:
            provider.set_administrator_role(owner, 'daily', True)
        granted_daily = web()
        granted_daily.oidc('daily' if provider else 'daily-admin')
        granted_daily.allowed_json('/api/users')
        if not provider:
            for actor in ('daily-missing', 'daily-empty', 'daily-unknown', 'daily-duplicate', 'daily-malformed', 'daily-string'):
                refused = web()
                refused.oidc(actor)
                refused.denied_json('/api/books')
        role_phase('demote')
        daily.denied_json('/settings/users')
        granted_daily.denied_json('/settings/users')
        conflicting = web()
        conflicting.oidc('daily' if provider else 'daily-admin')
        conflicting.denied_json('/api/books')
        if provider:
            provider.set_administrator_role(owner, 'daily', False)
        restored_daily = web()
        restored_daily.oidc('daily')
        restored_daily.request('/books')
        restored_daily.denied_json('/settings/users')
        if not provider:
            for actor in ('daily-missing', 'daily-empty'):
                ordinary = web()
                ordinary.oidc(actor)
                ordinary.allowed_json('/api/books')
                ordinary.denied_json('/settings/users')
            for actor in ('daily-unknown', 'daily-duplicate', 'daily-malformed', 'daily-string'):
                refused = web()
                refused.oidc(actor)
                refused.denied_json('/api/books')
            print('PASS: signed missing/empty ordinary roles and malformed/unknown/stale role refusal without authority overwrite.')
        for fields in ({'roles': ordinary_ids}, {'external_auth_id': 'foreign-subject'}):
            try:
                api.request('/api/users/' + str(initial_daily['user_id']), fields, method='PUT', token=admin_token)
                raise AssertionError('native writer changed managed authority')
            except urllib.error.HTTPError as error:
                if error.code != 403:
                    raise
        print('PASS: native owned roles, old-session grant/demotion, signed-role mismatch refusal and signed identity role consistency.')

        if create_mode and (producer or os.environ.get('KYQ_TEST_BOOKSTACK_CREATE_HTTP') == '1'):
            creation_token = secrets.token_hex(32)
            creation_policy = {'installation_id': '271333e2-c221-4601-b249-563b3d828530',
                               'issuer': issuer, 'client_id': 'fixture-client', 'everyday_role': 'editor',
                               'ordinary_role_ids': ordinary_ids}
            def stage_creation(name, value):
                if name not in ('creation.json', 'creation-policy.json'):
                    raise AssertionError('unknown owned creation file')
                result = subprocess.run(['docker', 'exec', '-i', '-u', 'abc', app, 'sh', '-c',
                                         'umask 077; cat > /config/kyquickstart-bridge/' + name],
                                        input=json.dumps(value).encode(), capture_output=True, timeout=10)
                if result.returncode:
                    raise AssertionError('protected creation staging failed')
            stage_creation('creation.json', {'token': creation_token})
            stage_creation('creation-policy.json', creation_policy)
            creation_probe = {'contract': 'ky.bookstack-creation/1', 'action': 'probe',
                              'installation_id': creation_policy['installation_id'], 'everyday_role': 'editor',
                              'ordinary_role_ids': ordinary_ids,
                              'account': {'contract': 'ky.bookstack-account/1', 'action': 'probe',
                                          'setup_id': '57a9c424-6499-4bfe-8f66-458b1c1136f0',
                                          'issuer': issuer, 'client_id': 'fixture-client',
                                          'subject': provider.actors['unassigned'], 'name': 'Created 雪',
                                          'email': 'unassigned@example.test', 'administrator': False}}
            for actor, token in ((owner, None), (api, bridge_token), (api, directory_token), (api, admin_token)):
                bridge_request(actor, token, creation_probe, expected=401, endpoint='creation')
            absent = bridge_request(api, creation_token, creation_probe, endpoint='creation')
            if absent.get('absent') is not True:
                raise AssertionError('native creation absence unverified')
            for bad in ({**creation_probe, 'installation_id': '2c676879-9c71-4913-b8a7-995521d9ed20'},
                        {**creation_probe, 'ordinary_role_ids': [1]},
                        {**creation_probe, 'account': {**creation_probe['account'], 'client_id': 'foreign'}},
                        {**creation_probe, 'account': {**creation_probe['account'], 'subject': 'trailing '}},
                        {**creation_probe, 'account': {**creation_probe['account'], 'administrator': True}},
                        {**creation_probe, 'account': {**creation_probe['account'], 'name': 'private-value' * 400}}):
                bridge_request(api, creation_token, bad, expected=400, endpoint='creation')
            for name in ('creation.json', 'creation-policy.json'):
                docker('exec', '-u', 'abc', app, 'chmod', '644', '/config/kyquickstart-bridge/' + name)
                bridge_request(api, creation_token, creation_probe, expected=503, endpoint='creation')
                docker('exec', '-u', 'abc', app, 'chmod', '600', '/config/kyquickstart-bridge/' + name)
            for token in (bridge_token, directory_token):
                stage_creation('creation.json', {'token': token})
                bridge_request(api, token, creation_probe, expected=503, endpoint='creation')
            stage_creation('creation.json', {'token': creation_token})
            bridge_request(api, creation_token, directory_check, expected=401, endpoint='directory')
            bridge_request(api, creation_token, {'contract': 'ky.bookstack-offboard/1', 'action': 'check',
                           'user_id': initial_daily['user_id'], 'issuer': issuer, 'subject': daily_spec['subject']}, expected=401)
            denied(api, '/api/books', token=creation_token)
            if bridge_request(api, creation_token, creation_probe, endpoint='creation') != absent:
                raise AssertionError('refused creation changed absence scope')
            print('PASS: separate HTTPS creation authority, protected pinned policy, bounded byte-exact requests and no content/directory authority.')

        if create_mode and not producer:
            creation_env = {**role_env, 'KYQ_TEST_BOOKSTACK_NEW_SUBJECT': provider.actors['unassigned']}
            def creation_phase(phase):
                native = subprocess.run(['go', 'test', '-race', '-v', '-count=1', './internal/adapters/bookstack',
                                         '-run', '^TestNativeManagedAccountCreation$'], capture_output=True, timeout=120,
                                        env={**creation_env, 'KYQ_TEST_BOOKSTACK_CREATE_PHASE': phase})
                print(native.stdout.decode().strip())
                if native.returncode or b'--- PASS: TestNativeManagedAccountCreation' not in native.stdout or b'--- SKIP:' in native.stdout:
                    raise AssertionError('native managed creation failed or skipped')
            creation_phase('create')
            provider.add_access(owner, 'unassigned')
            new_user = web()
            new_user.oidc('unassigned')
            new_user.request('/books')
            new_user.denied_json('/settings/users')
            users = json.loads(api.request('/api/users', token=admin_token))['data']
            matched = [user for user in users if user['email'] == 'unassigned@example.test']
            if len(matched) != 1:
                raise AssertionError('native creation lost its unique account')
            new_token = owner.token(matched[0]['id'])
            api.request('/api/books', token=new_token)
            creation_phase('retained')
            denied(new_user, '/books')
            denied(api, '/api/books', token=new_token)
            fresh_new = web()
            fresh_new.oidc('unassigned')
            fresh_new.denied_json('/api/books')
            print('PASS: new assigned subject uses the native ordinary account; revoke retains receipts and denies old/fresh access.')

        if os.environ.get('KYQ_TEST_BOOKSTACK_DIRECTORY') == '1' or producer:
            native_owner = json.loads(api.request('/api/users/' + str(initial_owner['user_id']), token=admin_token))
            owner_ordinary = sorted(role['id'] for role in native_owner['roles']
                                    if role['id'] != initialized_directory['roles']['administrator_role_id'])
            owner_profile = {**profile, 'user_id': initial_owner['user_id'], 'setup_id': owner_spec['setup_id'],
                             'subject': owner_spec['subject'], 'name': native_owner['name'], 'email': native_owner['email']}
            owner_roles = {**roles, 'user_id': initial_owner['user_id'], 'setup_id': owner_spec['setup_id'],
                           'subject': owner_spec['subject'], 'ordinary_role_ids': owner_ordinary, 'administrator': True}
            print('Native owner/everyday ordinary-role policies:', owner_ordinary, ordinary_ids)
            offboard(app, {'contract': 'ky.bookstack-directory/1', 'profile': owner_profile, 'roles': owner_roles},
                     command='kyquickstart:directory')
            if producer:
                producer.start(app, app_image, ca, issuer, origin, bridge_token, directory_token,
                               [(owner_spec, initial_owner['user_id'], owner_ordinary),
                                (daily_spec, initial_daily['user_id'], ordinary_ids)], ordinary_ids,
                               creation_token if create_mode else '', provider.actors['unassigned'] if create_mode else '')
                owner_revision = provider.wait_delivery(owner, 'owner')
                daily_revision = provider.wait_delivery(owner, 'daily')
                provider.set_profile(owner, 'owner', 'Producer owner', 'producer-owner@example.test')
                owner_revision = provider.wait_delivery(owner, 'owner', after=owner_revision)
                provider.set_profile(owner, 'daily', 'Directory owner 名', 'owner-directory@example.test')
                daily_revision = provider.wait_delivery(owner, 'daily', after=daily_revision)
                provider.set_administrator_role(owner, 'daily', True)
                daily_revision = provider.wait_delivery(owner, 'daily', after=daily_revision)
                restored_daily.request('/settings/users')
                provider.set_administrator_role(owner, 'daily', False)
                daily_revision = provider.wait_delivery(owner, 'daily', after=daily_revision)
            else:
                native = subprocess.run(['go', 'test', '-race', '-v', '-count=1', './internal/offboard',
                                         '-run', '^TestNativeBookStackDirectory$'], capture_output=True, timeout=120,
                                        env={**role_env, 'KYQ_TEST_BOOKSTACK_HTTP_CONTAINER': app,
                                             'KYQ_TEST_BOOKSTACK_OWNER_NATIVE_ID': str(initial_owner['user_id']),
                                             'KYQ_TEST_BOOKSTACK_OWNER_ORDINARY_ROLE_IDS': json.dumps(owner_ordinary)})
                print(native.stdout.decode().strip())
                if native.returncode or b'--- PASS: TestNativeBookStackDirectory' not in native.stdout or b'--- SKIP:' in native.stdout:
                    raise AssertionError('native resident BookStack directory failed or skipped')
            native_profile = json.loads(api.request('/api/users/' + str(initial_daily['user_id']), token=admin_token))
            if native_profile['name'] != 'Directory owner 名' or native_profile['email'] != 'owner-directory@example.test':
                raise AssertionError('resident directory changed no actual native profile')
            restored_daily.request('/books')
            restored_daily.denied_json('/api/users')
            fresh_directory = web()
            fresh_directory.oidc('daily')
            fresh_directory.request('/books')
            fresh_directory.denied_json('/api/users')
            print('PASS: actual identity worker native profile/grant/demotion and old/fresh ordinary access.' if producer else 'PASS: resident TLS SCIM profile/grant/demotion/repeat and reopened proofs; old/fresh native ordinary access verified.')
        if producer and create_mode:
            provider.add_access(owner, 'unassigned')
            born_revision = provider.wait_delivery(owner, 'unassigned')
            born = web()
            born.oidc('unassigned')
            born.request('/books')
            born.denied_json('/settings/users')
            users = json.loads(api.request('/api/users', token=admin_token))['data']
            matched = [user for user in users if user['email'] == 'unassigned@example.test']
            if len(matched) != 1:
                raise AssertionError('worker birth did not produce exactly one native account')
            born_id = matched[0]['id']
            born_token = owner.token(born_id)
            api.request('/api/books', token=born_token)
            provider.set_profile(owner, 'unassigned', 'Born worker 雪', 'born-worker@example.test')
            born_revision = provider.wait_delivery(owner, 'unassigned', after=born_revision)
            retained = json.loads(api.request('/api/users/' + str(born_id), token=admin_token))
            if retained['name'] != 'Born worker 雪' or retained['email'] != 'born-worker@example.test':
                raise AssertionError('born account could not resolve its projected native scope')
            born.request('/books')
            fresh_born = web()
            fresh_born.oidc('unassigned')
            fresh_born.request('/books')
            born_book = json.loads(api.request('/api/books', {'name': 'Born worker retained content',
                                   'description': 'Retained after global disablement'}, method='POST', token=born_token))
            provider.disable_user(owner, 'unassigned', 'owner')
            provider.wait_delivery(owner, 'unassigned', active=False, after=born_revision)
            denied(born, '/books')
            denied(api, '/api/books', token=born_token)
            try:
                web().oidc('unassigned')
                raise AssertionError('globally disabled born identity received fresh OIDC')
            except urllib.error.HTTPError as error:
                if error.code not in (401, 403):
                    raise
            retained_after = json.loads(api.request('/api/users/' + str(born_id), token=admin_token))
            if retained_after['id'] != born_id or retained_after['name'] != retained['name'] or retained_after['email'] != retained['email']:
                raise AssertionError('born revocation changed retained native identity/profile')
            if json.loads(api.request('/api/books/' + str(born_book['id']), token=admin_token))['description'] != born_book['description']:
                raise AssertionError('born revocation lost native content')
            proof = bridge_request(api, bridge_token, {'contract': 'ky.bookstack-offboard/1', 'action': 'check',
                                   'user_id': born_id, 'issuer': issuer, 'subject': provider.actors['unassigned']})
            if not proof['disabled'] or not proof['revocation_complete'] or proof['tokens_remaining'] != 0:
                raise AssertionError('born revocation lacks complete native fence proof')
            print('PASS: actual worker ordinary birth, projected scope resolution, native signed use, global revocation and retained identity/content.')

        token = owner.token(initial_daily['user_id'])
        book = json.loads(api.request('/api/books', {'name': 'Retained OIDC content', 'description': 'Preserved after revocation'}, method='POST', token=token))
        user_before = json.loads(api.request('/api/users/' + str(initial_daily['user_id']), token=admin_token))
        daily.request('/books')
        if provider:
            provider.remove_access(owner, 'daily')
            without_assignment = web()
            without_assignment.oidc('daily')
            without_assignment.denied_json('/api/books')
            if not producer and account(app, daily_spec)['disabled']:
                raise AssertionError('identity assignment removal silently mutated product state')
            # Native issuer refusal ends new issuance, not existing downstream sessions.
            # The product's Deprovision remains mandatory; the resident bridge is a gate.
            if producer:
                provider.wait_delivery(owner, 'daily', active=False, after=daily_revision)
                daily.denied_json('/api/books')
                denied(api, '/api/books', token=token)
            else:
                daily.request('/books')
                api.request('/api/books', token=token)
            print('PASS: actual identity worker assignment-loss revocation denies old sessions/tokens and fresh OIDC.' if producer else 'Native step-up assignment removal denies fresh OIDC; downstream revocation still requires its adapter.')
        binding = {'contract': 'ky.bookstack-offboard/1', 'action': 'deprovision', 'user_id': initial_daily['user_id'],
                   'issuer': issuer, 'subject': daily_spec['subject']}
        inspect = {**binding, 'action': 'check'}
        before = bridge_request(api, bridge_token, inspect)
        if before['disabled'] != bool(producer) or before['tokens_remaining'] != (0 if producer else 1):
            raise AssertionError('native scoped API initial inspection mismatch')
        bridge_request(owner, None, binding, expected=401)
        bridge_request(api, '0' * 64, binding, expected=401)
        bridge_request(api, directory_token, binding, expected=401)
        bridge_request(api, bridge_token, {**binding, 'subject': 'credential-must-not-reflect'}, expected=400)
        bridge_request(api, bridge_token, {**binding, 'subject': 'x' * 5000}, expected=400)
        foreign = json.loads(api.request('/api/users', {'name': 'Unowned fixture', 'email': 'foreign@example.test',
                             'external_auth_id': 'unowned-subject', 'roles': []}, method='POST', token=admin_token))
        bridge_request(api, bridge_token, {**binding, 'user_id': foreign['id'], 'subject': 'unowned-subject'}, expected=400)
        bridge_request(api, directory_token, {**directory_check, 'profile': {**directory_check['profile'], 'user_id': foreign['id'], 'subject': 'unowned-subject'},
                       'roles': {**directory_check['roles'], 'user_id': foreign['id'], 'subject': 'unowned-subject'}}, expected=400, endpoint='directory')
        docker('exec', '-u', 'abc', app, 'chmod', '644', '/config/kyquickstart-bridge/token.json')
        bridge_request(api, bridge_token, binding, expected=503)
        docker('exec', '-u', 'abc', app, 'chmod', '600', '/config/kyquickstart-bridge/token.json')
        if bridge_request(api, bridge_token, inspect) != before:
            raise AssertionError('refused RPC changed account authority')
        for scoped_token in (bridge_token, directory_token):
            request = urllib.request.Request(origin + '/api/books', headers={'Authorization': 'Bearer ' + scoped_token, 'Accept': 'application/json'})
            try:
                with api.opener.open(request, timeout=5):
                    raise AssertionError('scoped credential authorized product content')
            except urllib.error.HTTPError as error:
                if error.code not in (401, 403):
                    raise
        native_env = {**os.environ, 'KYQ_TEST_BOOKSTACK_HTTP_CONTAINER': app, 'KYQ_TEST_BOOKSTACK_FIXTURE_OWNER': suffix,
                      'KYQ_TEST_BOOKSTACK_USER_ID': str(initial_daily['user_id']), 'KYQ_TEST_BOOKSTACK_DAILY_SUBJECT': daily_spec['subject']}
        queue = os.environ.get('KYQ_TEST_BOOKSTACK_QUEUE') == '1'
        package, check = ('./internal/offboard', '^TestNativeBookStackQueue$') if queue else (
            './internal/adapters/bookstack', '^TestNativeHTTPOffboardingAdapter$')
        native = subprocess.run(['go', 'test', '-race', '-v', '-count=1', package,
                                 '-run', check], capture_output=True, timeout=120, env=native_env)
        print(native.stdout.decode().strip())
        if native.returncode:
            raise AssertionError('native scoped HTTP adapter qualification failed')
        denied(daily, '/books')
        denied(api, '/api/books', token=token)
        again = web()
        again.oidc('daily')
        again.denied_json('/api/books')
        retained = json.loads(api.request('/api/books/' + str(book['id']), token=admin_token))
        if retained['id'] != book['id'] or retained['description'] != book['description']:
            raise AssertionError('offboarding lost content')
        user_after = json.loads(api.request('/api/users/' + str(initial_daily['user_id']), token=admin_token))
        if any(user_after[key] != user_before[key] for key in ('id', 'external_auth_id', 'email', 'roles')):
            raise AssertionError('offboarding or denied login changed retained identity/roles')
        if account(app, daily_spec)['user_id'] != initial_daily['user_id']:
            raise AssertionError('OIDC denial replaced the retained identity')
        native_env = {**os.environ, 'KYQ_TEST_BOOKSTACK_OIDC_CONTAINER': app, 'KYQ_TEST_BOOKSTACK_FIXTURE_OWNER': suffix,
                      'KYQ_TEST_BOOKSTACK_OWNER_SUBJECT': owner_spec['subject'], 'KYQ_TEST_BOOKSTACK_DAILY_SUBJECT': daily_spec['subject']}
        native = subprocess.run(['go', 'test', '-race', '-v', '-count=1', './internal/adapters/bookstack',
                                 '-run', '^TestNativeOIDCAccountVerification$'],
                                capture_output=True, timeout=120, env=native_env)
        print(native.stdout.decode().strip())
        if native.returncode:
            raise AssertionError('signed OIDC Go-client verification failed')
        if not provider:
            with owner.opener.open(issuer + '/stats', timeout=5) as response:
                stats = json.loads(response.read(1024))
            if stats != {'authorizations': expected_exchanges[0], 'exchanges': expected_exchanges[0]}:
                raise AssertionError('a callback did not consume its exact native PKCE exchange')
            print('PASS: invalid signature refused through the native consumer.')
        print('PASS: ' + ('actual KyIdentity' if provider else 'signed fixture') + ' OIDC owner grant and everyday management denial; unassigned subject refusal.')
        retained_directory = bridge_request(api, directory_token, directory_check, endpoint='directory')
        if not retained_directory['profile']['disabled'] or not retained_directory['roles']['disabled']:
            raise AssertionError('HTTPS directory failed retained revoked readback')
        bridge_request(api, directory_token, {**directory_check, 'profile': {**directory_check['profile'], 'action': 'apply'},
                       'roles': {**directory_check['roles'], 'action': 'apply'}}, expected=400, endpoint='directory')
        role_phase('retained')
        print('PASS: offboarded OIDC session/token/new-login denial with retained identity and content.')
        if producer:
            surviving_administrator = web()
            provider.login(surviving_administrator, 'other-owner')
            owner_check = {'contract': 'ky.bookstack-directory/1',
                           'profile': {**owner_profile, 'action': 'check'},
                           'roles': {**owner_roles, 'action': 'check'}}
            owner_before = bridge_request(api, directory_token, owner_check, endpoint='directory')
            provider.disable_user(surviving_administrator, 'owner', 'other-owner')
            provider.wait_delivery(surviving_administrator, 'owner', active=False, after=owner_revision)
            denied(owner, '/settings/users')
            denied(api, '/api/books', token=admin_token)
            try:
                provider.request(owner, 'GET', '/api/admin/app-registry')
                raise AssertionError('disabled identity retained its old administrator session')
            except urllib.error.HTTPError as error:
                if error.code not in (401, 403):
                    raise
            try:
                web().oidc('owner')
                raise AssertionError('globally disabled identity obtained fresh OIDC')
            except urllib.error.HTTPError as error:
                if error.code not in (401, 403):
                    raise
            owner_retained = bridge_request(api, directory_token, owner_check, endpoint='directory')
            expected_owner = {**owner_before, 'profile': {**owner_before['profile'], 'disabled': True},
                              'roles': {**owner_before['roles'], 'disabled': True}}
            if owner_retained != expected_owner:
                raise AssertionError('global revocation changed retained owner profile/roles')
            owner_revocation = bridge_request(api, bridge_token, {**inspect, 'user_id': initial_owner['user_id'],
                                                                 'subject': owner_spec['subject']})
            if not owner_revocation['disabled'] or not owner_revocation['revocation_complete'] or owner_revocation['tokens_remaining'] != 0:
                raise AssertionError('global worker revocation lacks native durable denial')
            # Read-only native model proof after every assigned product administrator is disabled.
            code = r"require '/app/www/vendor/autoload.php'; $app=require '/app/www/bootstrap/app.php'; $app->make(\Illuminate\Contracts\Console\Kernel::class)->bootstrap();"
            code += r" $book=\BookStack\Entities\Models\Book::findOrFail(" + str(int(book['id'])) + r"); echo json_encode(['id'=>$book->id,'description'=>$book->description]);"
            retained_book = json.loads(docker('exec', '-u', 'abc', app, 'php', '-r', code))
            if retained_book != {'id': book['id'], 'description': book['description']}:
                raise AssertionError('global disablement lost native content')
            print('PASS: actual global disablement denies owner identity/product sessions, token and fresh OIDC; owner receipts/content and surviving identity administration retained.')
            producer.close()
            print('PASS: actual KyIdentity worker profile/role delivery, assignment-loss revocation and reopened resident proofs.')


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    try:
        main()
    except Exception as error:
        print('Native OIDC qualification failed:', type(error).__name__, file=sys.stderr)
        if isinstance(error, AssertionError):
            print(str(error), file=sys.stderr)
        if isinstance(error, urllib.error.HTTPError):
            print('Native HTTP status:', error.code, file=sys.stderr)
        sys.exit(1)
