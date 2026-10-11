"""Owned disposable KyIdentity provider; never connects to an existing service."""
import json
import os
import pathlib
import subprocess
import time
import urllib.request
import uuid

from qualification import cleanup, docker


class NativeIdentity:
    def __init__(self, owned, directory, binary, image, name, network, issuer, proxy_ip, producer=None, ca=None):
        executable = pathlib.Path(binary)
        if not executable.is_absolute() or not executable.is_file():
            raise AssertionError('native KyIdentity needs an explicit absolute local fixture binary')
        self.name, self.issuer = name, issuer
        self.data = directory
        self.producer, self.connector_id = producer, None
        directory.chmod(0o700)
        tls_args = []
        if producer:
            (directory / 'fixture-ca.pem').write_bytes(ca.read_bytes() + producer.cert.read_bytes())
            tls_args = ['-e', 'SSL_CERT_FILE=/data/fixture-ca.pem', '-e', 'KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true']
        docker('run', '-d', '--name', name, '--network', network, '--memory', '512m', '--memory-swap', '512m', '--cpus', '1', '--cap-drop', 'ALL',
               '--security-opt', 'no-new-privileges', '--user', str(os.getuid()) + ':' + str(os.getgid()),
               '--entrypoint', '/fixture/kyidentity', '-v', str(executable) + ':/fixture/kyidentity:ro',
               '-v', str(directory) + ':/data', '-e', 'KYIDENTITY_DATA_DIR=/data',
               '-e', 'KYIDENTITY_ISSUER_URL=' + issuer, '-e', 'TRUSTED_PROXY_CIDRS=' + proxy_ip + '/32', *tls_args, image)
        owned.callback(cleanup, 'rm', '-fv', name)
        deadline = time.monotonic() + 30
        while True:
            result = subprocess.run(['docker', 'exec', name, 'php', '-r',
                                     'exit(@file_get_contents("http://127.0.0.1:5867/readyz") === false ? 1 : 0);'],
                                    capture_output=True, timeout=5)
            if result.returncode == 0:
                break
            if time.monotonic() >= deadline:
                raise AssertionError('native KyIdentity startup failed')
            time.sleep(0.25)
        self.actors = {}

    def provision(self, origin):
        accounts = [('owner', 'daily', 'fixture-client'), ('other-owner', 'unassigned', None)]
        for administrator, daily, client_id in accounts:
            folder = self.data / ('setup-' + administrator)
            folder.mkdir(mode=0o700)
            for actor in (administrator, daily):
                (folder / (actor + '.password')).write_text('disposable-identity-' + actor + '-password')
                (folder / (actor + '.password')).chmod(0o600)
            (folder / 'client.secret').write_text('disposable-fixture-client-secret')
            (folder / 'client.secret').chmod(0o600)
            prefix = '/data/' + folder.name + '/'
            bundle = {'schema': 'kyidentity.setup/1', 'setupId': str(uuid.uuid4()),
                      'admin': {'username': administrator, 'email': administrator + '@example.test', 'passwordFile': prefix + administrator + '.password'},
                      'everyday': {'username': daily, 'email': daily + '@example.test', 'passwordFile': prefix + daily + '.password'},
                      'clients': []}
            if client_id:
                bundle['clients'] = [{'id': client_id, 'name': 'BookStack fixture', 'secretFile': prefix + 'client.secret',
                                      'redirectUris': [origin + '/oidc/callback'], 'postLogoutUris': [],
                                      'administratorRole': 'admin', 'everydayRole': 'editor'}]
            if client_id and self.producer:
                (folder / 'scim.token').write_text(self.producer.token)
                (folder / 'scim.token').chmod(0o600)
                bundle['clients'][0]['scim'] = {'url': self.producer.url, 'tokenFile': prefix + 'scim.token', 'groups': False}
            path = folder / 'bundle.json'
            path.write_text(json.dumps(bundle))
            path.chmod(0o600)
            remote = prefix + 'bundle.json'
            report = json.loads(docker('exec', self.name, '/fixture/kyidentity', 'apply-setup', '--file', remote))
            for obj in report['created']:
                if obj['kind'] == 'connector' and obj['name'] == 'fixture-client':
                    self.connector_id = obj['id']
                if obj['kind'] == 'user':
                    self.actors[obj['name']] = obj['id']
            for check in (['--check'], []):
                resumed = json.loads(docker('exec', self.name, '/fixture/kyidentity', 'apply-setup', '--file', remote, *check))
                if resumed['created'] or len(resumed['reused']) != len(report['created']):
                    raise AssertionError('native identity setup did not resume its owned resources')
        if set(self.actors) != {'owner', 'daily', 'other-owner', 'unassigned'}:
            raise AssertionError('native identity subject receipts missing')
        print('Native KyIdentity accounts/client/app roles created, checked and resumed.')

    def login(self, web, actor):
        with web.opener.open(self.issuer + '/api/auth/csrf', timeout=5) as response:
            csrf = json.loads(response.read(4096))['csrfToken']
        request = urllib.request.Request(self.issuer + '/api/auth/login',
                                         json.dumps({'username': actor, 'password': 'disposable-identity-' + actor + '-password'}).encode(),
                                         {'Content-Type': 'application/json', 'X-CSRF-Token': csrf})
        with web.opener.open(request, timeout=5) as response:
            result = json.loads(response.read(8192))
        if not result.get('success') or result.get('mfaRequired') or result.get('restricted'):
            raise AssertionError('native fixture identity login did not establish a normal session')

    def request(self, web, method, path, data=None, headers=None):
        with web.opener.open(self.issuer + '/api/auth/csrf', timeout=5) as response:
            csrf = json.loads(response.read(4096))['csrfToken']
        request = urllib.request.Request(self.issuer + path,
                                         None if data is None else json.dumps(data).encode(),
                                         {'Content-Type': 'application/json', 'X-CSRF-Token': csrf, **(headers or {})}, method=method)
        with web.opener.open(request, timeout=5) as response:
            raw = response.read((1 << 20) + 1)
            if len(raw) > 1 << 20:
                raise AssertionError('oversized native identity response')
            return None if not raw else json.loads(raw)

    def remove_access(self, administrator, actor):
        records = self.request(administrator, 'GET', '/api/admin/app-registry')['records']
        matched = [record for record in records if record['clientId'] == 'fixture-client']
        if len(matched) != 1 or matched[0]['accessMode'] != 'assigned_only':
            raise AssertionError('native client assignment source mismatch')
        path = '/api/admin/app-registry/' + matched[0]['id'] + '/assignments/users/' + self.actors[actor]
        grant = self.request(administrator, 'POST', '/api/auth/step-up',
                             {'password': 'disposable-identity-owner-password', 'operation': 'DELETE ' + path})
        if grant.get('kind') != 'grant' or not grant.get('stepUpToken'):
            raise AssertionError('native assignment removal did not obtain operation-bound step-up')
        result = self.request(administrator, 'DELETE', path, headers={'X-KyIdentity-StepUp': grant['stepUpToken']})
        if result != {'success': True}:
            raise AssertionError('native assignment removal failed')

    def set_administrator_role(self, administrator, actor, enabled):
        records = self.request(administrator, 'GET', '/api/admin/app-registry')['records']
        apps = [record for record in records if record['clientId'] == 'fixture-client']
        if len(apps) != 1 or apps[0]['accessMode'] != 'assigned_only':
            raise AssertionError('native role client scope ambiguous')
        roles = self.request(administrator, 'GET', '/api/admin/app-registry/' + apps[0]['id'] + '/roles')['roles']
        admins = [role for role in roles if role['name'] == 'admin']
        if len(admins) != 1:
            raise AssertionError('native administrator role ambiguous')
        path = '/api/admin/app-registry/' + apps[0]['id'] + '/roles/' + admins[0]['id'] + '/assignments/users/' + self.actors[actor]
        method = 'PUT' if enabled else 'DELETE'
        grant = self.request(administrator, 'POST', '/api/auth/step-up',
                             {'password': 'disposable-identity-owner-password', 'operation': method + ' ' + path})
        if grant.get('kind') != 'grant' or not grant.get('stepUpToken'):
            raise AssertionError('native role mutation lacks step-up')
        result = self.request(administrator, method, path, headers={'X-KyIdentity-StepUp': grant['stepUpToken']})
        if result != {'success': True}:
            raise AssertionError('native role mutation failed')

    def wait_delivery(self, administrator, actor, active=True, after=0):
        if not self.connector_id:
            raise AssertionError('producer native connector receipt missing')
        deadline = time.monotonic() + (140 if after == 0 else 70)
        while time.monotonic() < deadline:
            report = self.request(administrator, 'GET', '/api/admin/systems/' + self.connector_id + '/provisioning')
            for row in report['users']:
                if row['userId'] == self.actors[actor] and row['revision'] > after and row['desired'] == active and row['recorded'] == active and row['acknowledged'] and not row['blocked'] and (row.get('lastEvent') or {}).get('status') == 'delivered':
                    print('Actual identity delivery:', actor, active, 'revision', row['revision'])
                    return row['revision']
            time.sleep(0.1)
        for row in report['users']:
            event = row.get('lastEvent') or {}
            print('Producer state:', row['desired'], row['recorded'], row['acknowledged'], row['blocked'], row['revision'], event.get('status'), event.get('attempts'))
            for category in ('x509', 'connection refused', 'timeout', '400', '403', '404', '409', '503', 'uncertain', 'TLS'):
                if category in event.get('lastError', ''):
                    print('Producer failure category:', category)
        raise AssertionError('actual identity worker did not acknowledge lifecycle delivery')

    def set_profile(self, administrator, actor, name, email):
        path = '/api/admin/users/' + self.actors[actor]
        grant = self.request(administrator, 'POST', '/api/auth/step-up',
                             {'password': 'disposable-identity-owner-password', 'operation': 'PUT ' + path})
        if grant.get('kind') != 'grant' or not grant.get('stepUpToken'):
            raise AssertionError('native profile mutation lacks step-up')
        self.request(administrator, 'PUT', path, {'displayName': name, 'email': email},
                     headers={'X-KyIdentity-StepUp': grant['stepUpToken']})

    def disable_user(self, administrator, actor, administrator_actor):
        path = '/api/admin/users/' + self.actors[actor]
        grant = self.request(administrator, 'POST', '/api/auth/step-up',
                             {'password': 'disposable-identity-' + administrator_actor + '-password',
                              'operation': 'PUT ' + path})
        if grant.get('kind') != 'grant' or not grant.get('stepUpToken'):
            raise AssertionError('global disablement lacks operation-bound step-up')
        self.request(administrator, 'PUT', path, {'status': 'disabled'},
                     headers={'X-KyIdentity-StepUp': grant['stepUpToken']})

    def add_access(self, administrator, actor):
        records = self.request(administrator, 'GET', '/api/admin/app-registry')['records']
        matched = [record for record in records if record['clientId'] == 'fixture-client']
        if len(matched) != 1 or matched[0]['accessMode'] != 'assigned_only':
            raise AssertionError('native assignment scope ambiguous')
        path = '/api/admin/app-registry/' + matched[0]['id'] + '/assignments/users/' + self.actors[actor]
        grant = self.request(administrator, 'POST', '/api/auth/step-up',
                             {'password': 'disposable-identity-owner-password', 'operation': 'PUT ' + path})
        if grant.get('kind') != 'grant' or not grant.get('stepUpToken'):
            raise AssertionError('native assignment grant lacks step-up')
        self.request(administrator, 'PUT', path, headers={'X-KyIdentity-StepUp': grant['stepUpToken']})
