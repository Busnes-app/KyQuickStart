"""Owned raw-TCP relay and actual resident-service producer fixture."""
import base64
import json
import os
import pathlib
import re
import secrets
import socket
import ssl
import subprocess
import time
import urllib.request
import urllib.parse
from qualification import cleanup, docker


class NativeProducer:
    def __init__(self, owned, directory, network, binary):
        self.directory = directory
        directory.mkdir(mode=0o700)
        self.name = network.removesuffix('-net') + '-connector'
        image = os.environ.get('KYQ_TEST_BOOKSTACK_SSH_IMAGE', '')
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', image):
            raise AssertionError('producer requires immutable SSH fixture image')
        self.binary = binary
        self.token = secrets.token_hex(32)
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        for name in ('host_ed25519', 'client_ed25519'):
            subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(directory / name)], check=True, capture_output=True)
        (directory / 'authorized_keys').write_bytes((directory / 'client_ed25519.pub').read_bytes())
        (directory / 'authorized_keys').chmod(0o600)
        docker('run', '-d', '--name', self.name, '--network', network, '--memory', '64m', '--memory-swap', '64m', '--cpus', '1',
               '--label', 'kyq.fixture=bookstack', '--label', 'kyq.fixture-owner=' + network.split('-')[2],
               '-p', '127.0.0.1::22', '-v', str(directory) + ':/keys:ro', image,
               '-o', 'GatewayPorts=clientspecified', '-o', 'AllowTcpForwarding=remote')
        owned.callback(cleanup, 'rm', '-fv', self.name)
        endpoint = docker('port', self.name, '22').strip()
        if not re.fullmatch(r'127\.0\.0\.1:[0-9]+', endpoint):
            raise AssertionError('producer SSH endpoint must be loopback')
        sshport = endpoint.split(':')[1]
        public = (directory / 'host_ed25519.pub').read_text().split()
        known = directory / 'known_hosts'
        known.write_text('[127.0.0.1]:' + sshport + ' ' + ' '.join(public[:2]) + '\n')
        known.chmod(0o600)
        time.sleep(0.3)
        self.ssh = subprocess.Popen(['ssh', '-N', '-p', sshport, '-i', str(directory / 'client_ed25519'),
                                    '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
                                    '-o', 'UserKnownHostsFile=' + str(known), '-o', 'ExitOnForwardFailure=yes',
                                    '-o', 'ConnectTimeout=5', '-R', '0.0.0.0:' + str(self.port) + ':127.0.0.1:' + str(self.port),
                                    'root@127.0.0.1'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        owned.callback(self.close_ssh)
        time.sleep(0.3)
        if self.ssh.poll() is not None:
            raise AssertionError('producer raw TCP relay failed')
        self.cert, self.key = directory / 'server.pem', directory / 'server-key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
                        '-keyout', str(self.key), '-out', str(self.cert), '-days', '1', '-subj', '/CN=' + self.name,
                        '-addext', 'basicConstraints=critical,CA:FALSE', '-addext', 'extendedKeyUsage=serverAuth',
                        '-addext', 'keyUsage=digitalSignature', '-addext', 'subjectAltName=DNS:' + self.name + ',IP:127.0.0.1'], check=True, capture_output=True)
        self.key.chmod(0o600)
        self.url = 'https://' + self.name + ':' + str(self.port) + '/scim/wiki'
        self.process = None
        self.stopped = False
        owned.callback(self.close)

    def close_ssh(self):
        if self.ssh.poll() is None:
            self.ssh.terminate()
            try:
                self.ssh.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.ssh.kill()
                self.ssh.wait()

    def start(self, app, image, ca, issuer, origin, product_token, directory_token, accounts, ordinary_ids, creation_token="", new_subject=""):
        bindings = []
        scopes = {}
        for spec, native_id, role_ids in accounts:
            bindings.append({'setup_id': '271333e2-c221-4601-b249-563b3d828530', 'app': 'wiki', 'kind': 'bookstack',
                             'origin': origin, 'issuer': issuer, 'subject': spec['subject'], 'native_id': native_id})
            scopes[spec['subject']] = {'native_id': native_id, 'account_setup_id': spec['setup_id'], 'ordinary_role_ids': role_ids}
        encoded = lambda data: base64.b64encode(data).decode()
        data = {'Container': app, 'Image': image, 'Token': product_token, 'DirectoryToken': directory_token,
                'Listen': '127.0.0.1:' + str(self.port), 'scim_token': self.token, 'CA': encoded(ca.read_bytes()),
                'Certificate': encoded(self.cert.read_bytes()), 'Key': encoded(self.key.read_bytes()), 'Bindings': bindings,
                'BookStack': {'client_id': 'fixture-client', 'ordinary_role_ids': ordinary_ids, 'accounts': scopes}}
        if new_subject:
            data.update(CreationToken=creation_token, new_subject=new_subject)
        self.output = open(self.directory / 'resident.log', 'w+b')
        self.process = subprocess.Popen([str(self.binary), '-test.run=^TestNativeIdentityProducerBridge$', '-test.v'],
                                        stdin=subprocess.PIPE, stdout=self.output, stderr=self.output,
                                        env={**os.environ, 'KYQ_TEST_BOOKSTACK_PRODUCER': '1'})
        self.process.stdin.write(json.dumps(data).encode() + b'\n')
        self.process.stdin.flush()
        trust = ssl.create_default_context(cafile=str(self.cert))
        observer = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=trust))
        request = urllib.request.Request('https://127.0.0.1:' + str(self.port) + '/scim/wiki/Users?filter=' + urllib.parse.quote('externalId eq ' + json.dumps(accounts[0][0]['subject'])) + '&startIndex=1&count=2',
                                         headers={'Authorization': 'Bearer ' + self.token})
        for _ in range(100):
            if self.process.poll() is not None:
                raise AssertionError('producer resident exited before ready')
            try:
                with observer.open(request, timeout=1) as response:
                    if response.status == 200:
                        return
            except OSError:
                pass
            time.sleep(0.1)
        raise AssertionError('producer resident did not become ready')

    def close(self):
        if self.process is None or self.stopped:
            return
        self.stopped = True
        if self.process.poll() is None:
            self.process.stdin.write(b'{"stop":true}\n')
            self.process.stdin.flush()
            self.process.stdin.close()
            try:
                self.process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
                raise AssertionError('producer resident failed to join')
        self.output.seek(0)
        output = self.output.read()
        self.output.close()
        print(output.decode().strip())
        if self.process.returncode or b'--- PASS: TestNativeIdentityProducerBridge' not in output or b'--- SKIP:' in output:
            raise AssertionError('producer resident audit qualification failed')
