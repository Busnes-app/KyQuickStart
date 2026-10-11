'use strict';
// Actual KyIdentity authorization and native LibreChat Passport/PKCE callback.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const https = require('node:https');
const { spawn, execFileSync } = require('node:child_process');
const nativeRequire = require('node:module').createRequire('/app/package.json');
const mongoose = nativeRequire('mongoose');
const { createModels, createMethods, runAsSystem } = nativeRequire('@librechat/data-schemas');
const core = require('./revocation.cjs');
const listen = server => new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
const stop = server => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); });

async function main() {
  mongoose.set('autoIndex', false); mongoose.set('autoCreate', false);
  await mongoose.connect(process.env.MONGO_URI, { autoIndex: false, autoCreate: false, maxPoolSize: 1 });
  require('./writers.cjs').register(mongoose);
  const models = createModels(mongoose);
  const native = { ...models, ...createMethods(mongoose), mongoose, runAsSystem };
  const probe = await require('./jwt.native.cjs')(native);
  const proxy = https.createServer({ key: fs.readFileSync('/fixture/oidc-key.pem'), cert: fs.readFileSync('/fixture/oidc-cert.pem') }, (req, res) => {
    const upstream = http.request({ hostname: '127.0.0.1', port: 5867, path: req.url, method: req.method,
      headers: { ...req.headers, 'x-forwarded-proto': 'https' } }, reply => {
      res.writeHead(reply.statusCode, reply.headers); reply.pipe(res);
    });
    upstream.on('error', () => res.writeHead(503).end()); req.pipe(upstream);
  });
  await listen(proxy);
  const issuer = 'https://127.0.0.1:' + proxy.address().port;
  const directory = '/fixture/browser-identity'; fs.mkdirSync(directory, { mode: 0o700 });
  const identityEnv = { ...process.env, KYIDENTITY_DATA_DIR: directory, KYIDENTITY_ISSUER_URL: issuer,
    KYIDENTITY_ALLOW_PRIVATE_CALLBACKS: 'true', SSL_CERT_FILE: '/fixture/oidc-cert.pem',
    TRUSTED_PROXY_CIDRS: '127.0.0.1/32', KYIDENTITY_LISTEN_HOST: '127.0.0.1', GOMAXPROCS: '1', GOMEMLIMIT: '128MiB' };
  const identity = spawn(process.env.KYQ_TEST_IDENTITY_BINARY, [], { env: identityEnv, stdio: 'ignore' });
  let appServer;
  try {
    for (let attempt = 0; ; attempt++) {
      if (identity.exitCode != null) { throw new Error('Owned identity exited'); }
      const ready = await fetch(issuer + '/readyz').catch(() => null);
      if (ready?.status === 200) { await ready.arrayBuffer(); break; }
      if (attempt === 100) { throw new Error('Owned identity startup refused'); }
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    const passport = nativeRequire('passport');
    const app = nativeRequire('express')();
    app.use(nativeRequire('express-session')({ secret: 'disposable-browser-session-secret', resave: false, saveUninitialized: false }));
    app.use(passport.initialize());
    app.get('/login', passport.authenticate('openid', { session: false }));
    app.get('/callback', passport.authenticate('openid', { session: false }), async (req, res, next) => {
      try { await probe.issue(req.user._id); res.json({ id: String(req.user._id), role: req.user.role }); } catch (error) { next(error); }
    });
    app.use((error, req, res, next) => res.status(error.status === 403 ? 403 : 500).end());
    appServer = https.createServer({ key: fs.readFileSync('/fixture/oidc-key.pem'), cert: fs.readFileSync('/fixture/oidc-cert.pem') }, app); await listen(appServer);
    const origin = 'https://127.0.0.1:' + appServer.address().port;
    const write = (name, value) => { const path = directory + '/' + name; fs.writeFileSync(path, value, { mode: 0o600 }); return path; };
    const actors = ['browser-owner', 'browser-daily'];
    for (const actor of actors) { write(actor + '.password', 'disposable-' + actor + '-password'); }
    const secret = 'disposable-browser-client-secret'; write('client.secret', secret);
    const account = actor => ({ username: actor, email: actor + '@example.test', passwordFile: directory + '/' + actor + '.password' });
    const bundle = { schema: 'kyidentity.setup/1', setupId: require('node:crypto').randomUUID(),
      admin: account(actors[0]), everyday: account(actors[1]), clients: [{ id: 'browser-client', name: 'LibreChat browser fixture',
        secretFile: directory + '/client.secret', redirectUris: [origin + '/callback'], postLogoutUris: [], administratorRole: 'admin', everydayRole: 'user' }] };
    write('bundle.json', JSON.stringify(bundle));
    const report = JSON.parse(execFileSync(process.env.KYQ_TEST_IDENTITY_BINARY, ['apply-setup', '--file', directory + '/bundle.json'], { env: identityEnv, timeout: 10000 }));
    const subjects = Object.fromEntries(report.created.filter(item => item.kind === 'user').map(item => [item.name, item.id]));
    assert.ok(subjects[actors[0]] && subjects[actors[1]]);
    process.env.OPENID_ISSUER = issuer; process.env.OPENID_CLIENT_ID = 'browser-client'; process.env.OPENID_CLIENT_SECRET = secret;
    process.env.OPENID_USE_PKCE = 'true'; process.env.OPENID_SCOPE = 'openid profile email';
    process.env.OPENID_ADMIN_ROLE = 'admin'; process.env.OPENID_ADMIN_ROLE_PARAMETER_PATH = 'roles'; process.env.OPENID_ADMIN_ROLE_TOKEN_KIND = 'id';
    process.env.DOMAIN_SERVER = origin; process.env.OPENID_CALLBACK_URL = '/callback';
    const strategy = probe.load('api/strategies/openidStrategy.js', { registration: { allowedDomains: [] } });
    assert.ok(await strategy.setupOpenId());
    const ids = ['0123456789abcdef01234571', '0123456789abcdef01234572'];
    await runAsSystem(() => models.User.create({ _id: ids[0], provider: 'openid', openidId: subjects[actors[0]],
      openidIssuer: issuer, role: 'USER', email: actors[0] + '@example.test', emailVerified: true }));
    async function login(actor) {
      const cookies = new Map();
      async function request(url, options = {}) {
        const parsed = new URL(url); const cookie = cookies.get(parsed.origin) || '';
        const response = await fetch(url, { redirect: 'manual', ...options, headers: { ...options.headers, ...(cookie ? { cookie } : {}) } });
        const saved = response.headers.getSetCookie().map(value => value.split(';')[0]).join('; ');
        if (saved) { cookies.set(parsed.origin, saved); }
        return response;
      }
      const csrfResponse = await request(issuer + '/api/auth/csrf'); const csrf = (await csrfResponse.json()).csrfToken;
      const authenticated = await request(issuer + '/api/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: issuer, 'X-CSRF-Token': csrf },
        body: JSON.stringify({ username: actor, password: 'disposable-' + actor + '-password' }) });
      assert.equal(authenticated.status, 200); assert.equal((await authenticated.json()).success, true);
      const start = await request(origin + '/login'); assert.equal(start.status, 302); await start.arrayBuffer();
      const authorization = new URL(start.headers.get('location')); assert.equal(authorization.origin, issuer);
      assert.equal(authorization.searchParams.get('code_challenge_method'), 'S256');
      const authorized = await request(authorization); assert.equal(authorized.status, 302); await authorized.arrayBuffer();
      const callback = new URL(authorized.headers.get('location')); assert.equal(callback.origin + callback.pathname, origin + '/callback');
      assert.ok(callback.searchParams.get('code'));
      const result = await request(callback); const raw = await result.text();
      return { status: result.status, raw };
    }
    const daily = await login(actors[1]); assert.equal(daily.status, 200);
    const dailyReport = JSON.parse(daily.raw); assert.match(dailyReport.id, /^[0-9a-f]{24}$/); assert.equal(dailyReport.role, 'USER');
    ids[1] = dailyReport.id;
    const owner = await login(actors[0]); assert.equal(owner.status, 200); assert.equal(JSON.parse(owner.raw).role, 'ADMIN');
    const request = { contract: core.contract, action: 'revoke', user_id: ids[1], issuer, subject: subjects[actors[1]] };
    await core.revoke(request, native, issuer);
    const before = await models.User.collection.find({}).sort({ _id: 1 }).toArray();
    const sessions = await models.Session.collection.find({}).sort({ _id: 1 }).toArray();
    assert.equal((await login(actors[1])).status, 403);
    assert.deepEqual(await models.User.collection.find({}).sort({ _id: 1 }).toArray(), before);
    assert.deepEqual(await models.Session.collection.find({}).sort({ _id: 1 }).toArray(), sessions);
    assert.equal((await login(actors[0])).status, 200);
    console.log('PASS: actual KyIdentity HTTPS discovery, assigned authorization, PKCE and native LibreChat Passport callback create an ordinary account and project the signed app administrator role; fresh revoked-subject login refuses without native User/session changes and control login remains accepted.');
    console.log('LIMIT: fixture config, in-memory Keyv cache and unused avatar/email boundaries; not full native routes, administrator capability/purpose qualification, initial setup ownership or production release.');
  } finally {
    identity.kill('SIGTERM');
    await Promise.race([new Promise(resolve => identity.once('exit', resolve)), new Promise(resolve => setTimeout(resolve, 2000))]);
    if (identity.exitCode == null) { identity.kill('SIGKILL'); }
    if (appServer) { await stop(appServer); } await stop(proxy); await probe.close();
    await mongoose.disconnect();
  }
}
main().catch(async error => { console.error('Owned browser qualification failed', error); await mongoose.disconnect(); process.exitCode = 1; });
