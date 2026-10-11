#!/usr/bin/env python3
"""Real pinned native model/CLI qualification; never uses a live deployment."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time
import uuid


def run(argv, **kwargs):
    result = subprocess.run(argv, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, timeout=60, **kwargs)
    if result.returncode:
        raise RuntimeError('disposable native fixture command failed: ' + result.stderr)
    return result.stdout


def main():
    source = Path(os.environ['KYQ_TEST_LIBRECHAT_SOURCE']).resolve()
    node = os.environ['KYQ_TEST_LIBRECHAT_NODE_IMAGE']
    mongo = os.environ['KYQ_TEST_LIBRECHAT_MONGO_IMAGE']
    for image in (node, mongo):
        if not re.fullmatch(r'(?:[a-z0-9./_-]+@)?sha256:[0-9a-f]{64}', image):
            raise ValueError('native fixture requires immutable image digests')
    commit = run(['git', '-C', str(source), 'rev-parse', 'HEAD']).strip()
    if commit != 'e8f3be08623663d4ad7f7241e693c94469b63bb0':
        raise ValueError('wrong native source commit')
    run(['git', '-C', str(source), 'diff', 'HEAD', '--exit-code'])
    for artifact in ('packages/data-provider/dist/index.js',
                     'packages/data-schemas/dist/index.cjs'):
        if not (source / artifact).is_file():
            raise ValueError('build pinned native packages first')
    module = Path(__file__).resolve().parent / 'module'
    identity = os.environ.get('KYQ_TEST_IDENTITY_BINARY')
    if identity and (not Path(identity).is_absolute() or not Path(identity).is_file()):
        raise ValueError('native browser fixture requires an explicit absolute KyIdentity binary')
    identity_mount = ['-v', identity + ':/identity/kyidentity:ro',
                      '-e', 'KYQ_TEST_IDENTITY_BINARY=/identity/kyidentity'] if identity else []
    owner = f'{os.getuid()}:{os.getgid()}'
    name = 'kyq-librechat-native-' + uuid.uuid4().hex[:12]
    network, db, worker = name, name + '-db', name + '-worker'
    made_network = False
    with tempfile.TemporaryDirectory(prefix=name + '.', dir=os.environ['TMPDIR']) as directory:
        state = Path(directory)
        os.chmod(state, 0o700)
        (state / 'logs').mkdir(mode=0o700)
        run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
             '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1',
             '-keyout', str(state / 'oidc-key.pem'), '-out', str(state / 'oidc-cert.pem')])
        for filename in ('oidc-key.pem', 'oidc-cert.pem'):
            os.chmod(state / filename, 0o600)
        overlay = state / 'overlay'
        # Apply to owned copies, never to the pinned native checkout.
        patch = (module / 'source.patch').read_text()
        for relative in re.findall(r'^--- a/(.+)$', patch, re.MULTILINE):
            destination = overlay / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source / relative, destination)
        run(['git', 'apply', str(module / 'source.patch')], cwd=overlay)
        try:
            run(['docker', 'network', 'create', network]); made_network = True
            run(['docker', 'run', '-d', '--name', db, '--network', network,
                 '--network-alias', 'native-mongo', '--memory', '768m', '--memory-swap', '768m',
                 '--cpus', '1', '--pids-limit', '128', mongo, 'mongod', '--bind_ip_all',
                 '--wiredTigerCacheSizeGB', '0.25', '--quiet'])
            ready = False
            for _ in range(40):
                ping = subprocess.run(['docker', 'exec', db, 'mongosh', '--quiet', '--eval',
                                       'quit(db.adminCommand({ping:1}).ok === 1 ? 0 : 1)'],
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
                if ping.returncode == 0:
                    ready = True; break
                time.sleep(0.25)
            if not ready:
                raise RuntimeError('disposable native MongoDB did not become ready')
            run(['docker', 'run', '-d', '--name', worker, '--network', network,
                 '--memory', '512m', '--memory-swap', '512m', '--cpus', '1', '--pids-limit', '128',
                 '--user', owner, '-e', 'NODE_OPTIONS=--max-old-space-size=256',
                 '-e', 'MONGO_URI=mongodb://native-mongo:27017/kyquickstart_fixture',
                 '-e', 'OPENID_ISSUER=https://identity.example.test', '-e', 'OPENID_REUSE_TOKENS=false',
                 '-e', 'LIBRECHAT_LOG_DIR=/fixture/logs',
                 '-e', 'NODE_EXTRA_CA_CERTS=/fixture/oidc-cert.pem',
                 '-e', 'USE_REDIS=false', '-e', 'MONGO_AUTO_INDEX=false', '-e', 'MONGO_AUTO_CREATE=false',
                 '-v', str(source) + ':/app:ro', '-v', str(module) + ':/module:ro',
                 '-v', str(state) + ':/fixture',
                 '-v', str(overlay) + ':/overlay:ro',
                 *identity_mount,
                 '-w', '/app', node, 'sleep', '600'])
            for relative in re.findall(r'^--- a/(.+)$', patch, re.MULTILINE):
                run(['docker', 'exec', worker, 'node', '--check', '/overlay/' + relative])
            script = r"""
const {createRequire}=require('node:module');
const nativeRequire=createRequire('/app/package.json');
const mongoose=nativeRequire('mongoose');
mongoose.set('autoIndex',false); mongoose.set('autoCreate',false);
const {createModels,runAsSystem}=nativeRequire('@librechat/data-schemas');
async function main(){
 await mongoose.connect(process.env.MONGO_URI,{autoIndex:false,autoCreate:false,maxPoolSize:1});
 const {User}=createModels(mongoose);
 if(process.argv[2]==='seed'){
  await runAsSystem(()=>User.create([
   {_id:'0123456789abcdef01234567',email:'daily@example.test',name:'Retained native 雪',emailVerified:true,
    provider:'openid',openidId:'subject-雪',openidIssuer:'https://identity.example.test',role:'USER',tenantId:'fixture-suite'},
   {_id:'0123456789abcdef01234568',email:'owner@example.test',emailVerified:true,
    provider:'openid',openidId:'owner-subject',openidIssuer:'https://identity.example.test',role:'ADMIN',tenantId:'fixture-suite'},
   {_id:'0123456789abcdef01234569',email:'local@example.test',emailVerified:true,provider:'local',role:'USER'}
  ]));
 }
 const collections=await mongoose.connection.db.listCollections().toArray();
 const snapshot=[];
 for(const item of collections.sort((a,b)=>a.name.localeCompare(b.name))){
  const collection=mongoose.connection.db.collection(item.name);
  snapshot.push({name:item.name,options:item.options,
   indexes:await collection.listIndexes().toArray(),
   docs:await collection.find({}).sort({_id:1}).toArray()});
 }
 console.log(JSON.stringify(snapshot));
 await mongoose.disconnect();
}
main().catch(async error=>{console.error('disposable native snapshot failed',error);await mongoose.disconnect();process.exitCode=1;});
"""
            (state / 'snapshot.cjs').write_text(script)
            os.chmod(state / 'snapshot.cjs', 0o600)
            def snapshot(action):
                raw = run(['docker', 'exec', worker, 'node', '/fixture/snapshot.cjs', action])
                return json.loads(raw)
            before = snapshot('seed')
            requests = [
                ('0123456789abcdef01234567', 'subject-雪', 'USER'),
                ('0123456789abcdef01234568', 'owner-subject', 'ADMIN'),
                ('0123456789abcdef01234567', 'foreign-subject', None),
                ('0123456789abcdef01234569', 'subject-雪', None),
                ('111111111111111111111111', 'subject-雪', None),
            ]
            for native_id, subject, role in requests:
                request = {'contract': 'ky.librechat-account-inspect/1', 'user_id': native_id,
                           'issuer': 'https://identity.example.test', 'subject': subject}
                (state / 'request.json').write_text(json.dumps(request))
                os.chmod(state / 'request.json', 0o600)
                result = subprocess.run(['docker', 'exec', worker, 'node', '/module/command.cjs',
                                         '--file', '/fixture/request.json'], stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, text=True, timeout=30)
                if role:
                    if result.returncode:
                        raise RuntimeError('native inspection failed: ' + result.stderr)
                    report = json.loads(result.stdout)
                    assert report['user_id'] == native_id and report['role'] == role
                    assert subject not in result.stdout and '@example.test' not in result.stdout
                else:
                    assert result.returncode != 0 and not result.stdout
                    assert result.stderr == 'LibreChat native inspection refused\n'
            assert snapshot('check') == before, 'inspection changed native collections/indexes/documents'
            print('PASS: actual native MongoDB/model/CLI ordinary/admin inspection and foreign/local/missing refusal; no collection, index or document changes.', flush=True)
            env = os.environ.copy()
            env['KYQ_TEST_LIBRECHAT_CONTAINER'] = worker
            env['KYQ_TEST_LIBRECHAT_USER'] = owner
            print(run(['go', 'test', '-race', '-v', '-count=1', './internal/adapters/librechat',
                       '-run', '^TestNativeMongoInspectionClient$'], env=env), flush=True)
            assert snapshot('check') == before, 'SDK inspection changed native database'
            names = sorted(entry.name for entry in state.iterdir())
            assert names == ['logs', 'oidc-cert.pem', 'oidc-key.pem', 'overlay', 'request.json', 'snapshot.cjs'], 'SDK retained private staging files'
            print('PASS: actual Go SDK/container command repeat/refusal and staging cleanup preserve all native database state.', flush=True)
            print(run(['docker', 'exec', worker, 'node', '/module/revocation.native.cjs']), flush=True)
            print(run(['docker', 'exec', worker, 'node', '/module/credential-recovery.native.cjs']), flush=True)
            print(run(['docker', 'exec', worker, 'node', '/module/credential-lifecycle.native.cjs']), flush=True)
            print(run(['docker', 'exec', worker, 'node', '/module/refresh-controller.native.cjs']), flush=True)
            if identity:
                print(run(['docker', 'exec', worker, 'node', '/module/browser.native.cjs']), flush=True)
            else:
                print('SKIP: actual KyIdentity/browser OIDC needs KYQ_TEST_IDENTITY_BINARY.', flush=True)
        finally:
            for container in (worker, db):
                subprocess.run(['docker', 'rm', '-f', '-v', container], stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL, timeout=15)
            if made_network:
                subprocess.run(['docker', 'network', 'rm', network], stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL, timeout=15)


if __name__ == '__main__':
    main()
