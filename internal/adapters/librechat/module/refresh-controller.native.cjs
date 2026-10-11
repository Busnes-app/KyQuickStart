'use strict';
// Actual native local-session controller over owned isolated HTTP/MongoDB.
const assert = require('node:assert/strict');
const { createRequire } = require('node:module');
const nativeRequire = createRequire('/app/package.json');
const mongoose = nativeRequire('mongoose');
const { createModels, createMethods, runAsSystem, tenantStorage } = nativeRequire('@librechat/data-schemas');
const core = require('./revocation.cjs');
const issuer = 'https://identity.example.test';
const daily = '0123456789abcdef01234580';
const owner = '0123456789abcdef01234581';
let server, probe;

async function main() {
  mongoose.set('autoIndex', false); mongoose.set('autoCreate', false);
  await mongoose.connect(process.env.MONGO_URI, { autoIndex: false, autoCreate: false, maxPoolSize: 1 });
  require('./writers.cjs').register(mongoose);
  const models = createModels(mongoose);
  const native = { ...models, ...createMethods(mongoose), mongoose, runAsSystem };
  for (const [id, role] of [[daily, 'USER'], [owner, 'ADMIN']]) {
    await models.User.collection.insertOne({ _id: new mongoose.Types.ObjectId(id), provider: 'openid',
      openidIssuer: issuer, openidId: 'refresh-' + id, role, name: 'Retained native refresh account', tenantId: 'fixture-suite' });
  }
  probe = await require('./jwt.native.cjs')(native);
  const auth = probe.load('api/server/services/AuthService.js');
  const controller = probe.load('api/server/controllers/AuthController.js');
  const express = nativeRequire('express');
  const app = express();
  app.use((req, res, next) => tenantStorage.run({ tenantId: 'fixture-suite' }, next));
  app.post('/auth/refresh', controller.refreshController);
  server = await new Promise(resolve => { const active = app.listen(0, '127.0.0.1', () => resolve(active)); });
  const refresh = async (token, query = '') => {
    const response = await fetch('http://127.0.0.1:' + server.address().port + '/auth/refresh' + query,
      { method: 'POST', redirect: 'manual', headers: token ? { cookie: 'refreshToken=' + token } : {} });
    const text = await response.text();
    const cookies = response.headers.getSetCookie();
    const changed = cookies.find(cookie => cookie.startsWith('refreshToken='));
    return { status: response.status, text, cookies,
      refreshToken: changed ? nativeRequire('cookie').parse(changed.split(';')[0]).refreshToken : undefined };
  };
  const snapshot = async id => ({
    user: await models.User.collection.findOne({ _id: new mongoose.Types.ObjectId(id) }),
    sessions: await models.Session.collection.find({ user: new mongoose.Types.ObjectId(id) }).sort({ _id: 1 }).toArray(),
  });
  // Issue in the same ordinary tenant context as the HTTP controller. System
  // context issuance would create an unscoped session invisible to tenant reads.
  const issue = async id => {
    const cookies = {};
    const access = await tenantStorage.run({ tenantId: 'fixture-suite', userId: id },
      () => auth.setAuthTokens(id, { cookie: (name, value) => { cookies[name] = value; } }));
    return { access, refresh: cookies.refreshToken };
  };
  const issued = await issue(daily);
  const control = await issue(owner);
  const accepted = await refresh(issued.refresh);
  assert.equal(accepted.status, 200);
  const body = JSON.parse(accepted.text);
  assert.equal(body.user._id, daily); assert.equal(await probe.authenticate(body.token), daily);
  assert.ok(accepted.cookies.some(cookie => cookie.startsWith('refreshToken=')));
  const dailyRefresh = accepted.refreshToken;
  assert.equal(typeof dailyRefresh, 'string');
  assert.ok(await runAsSystem(() => native.findSession({ userId: daily, refreshToken: dailyRefresh })));
  assert.equal(await probe.authenticate(control.access), owner);
  let ownerRefresh = control.refresh;
  const controlRefresh = async () => {
    const response = await refresh(ownerRefresh); assert.equal(response.status, 200);
    assert.equal(typeof response.refreshToken, 'string'); ownerRefresh = response.refreshToken;
  };
  await controlRefresh();
  assert.equal((await refresh()).status, 200);
  const beforeBad = await snapshot(daily);
  assert.equal((await refresh(dailyRefresh.slice(0, -4) + 'xxxx')).status, 403);
  assert.deepEqual(await snapshot(daily), beforeBad);
  const original = await snapshot(daily);
  const request = { contract: core.contract, action: 'revoke', user_id: daily, issuer, subject: 'refresh-' + daily };
  const cleanup = native.deleteAllUserSessions;
  native.deleteAllUserSessions = async () => { throw new Error('Owned fixture deferred cleanup'); };
  try { await assert.rejects(core.revoke(request, native, issuer), /requires inspection/); }
  finally { native.deleteAllUserSessions = cleanup; }
  const fenced = await snapshot(daily);
  assert.deepEqual(fenced.sessions, original.sessions);
  const beforeProfile = { ...fenced.user }; delete beforeProfile.kyquickstartRevocation;
  assert.deepEqual(beforeProfile, original.user);
  assert.ok(await runAsSystem(() => native.findSession({ userId: daily, refreshToken: dailyRefresh })));
  const denied = await refresh(dailyRefresh);
  assert.equal(denied.status, 403); assert.equal(denied.cookies.length, 0);
  assert.equal(denied.text.includes(daily), false);
  assert.deepEqual(await snapshot(daily), fenced);
  assert.equal((await refresh(dailyRefresh, '?retry=true')).status, 403);
  assert.deepEqual(await snapshot(daily), fenced);
  await controlRefresh();
  assert.equal((await core.revoke(request, native, issuer)).session_and_agent_keys_cleared, true);
  const clean = await snapshot(daily);
  assert.equal(clean.sessions.length, 0);
  assert.ok([401, 403].includes((await refresh(dailyRefresh)).status));
  assert.deepEqual(await snapshot(daily), clean);
  await controlRefresh();
  console.log('PASS: actual native AuthController local-session refresh over HTTP accepts signed retained sessions before publication, rejects invalid signatures, denies revoked sessions before cleanup without cookies/user/session mutation, remains denied after cleanup and preserves control refresh access.');
  console.log('LIMIT: fixture tenant configuration and refusing unused provider/Graph boundaries; no provider-token reuse, refresh bridge, full server routing, initial setup ownership or administrator purpose qualification.');
}
async function close() {
  if (server) { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
  if (probe) { await probe.close(); }
  await mongoose.disconnect();
}
main().then(close).catch(async error => {
  await close(); process.stderr.write('LibreChat native refresh controller qualification failed\n');
  for (const line of String(error.stack).split('\n').filter(line => /^\s+at /.test(line) && line.includes('/module/')).slice(0, 3)) {
    process.stderr.write(line + '\n');
  }
  process.exitCode = 1;
});
