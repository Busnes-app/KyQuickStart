'use strict';
// Owned disposable MongoDB/Node fixture; never an installed maintenance command.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const crypto = require('node:crypto');
const { spawn } = require('node:child_process');
const { createRequire } = require('node:module');
const nativeRequire = createRequire('/app/package.json');
const mongoose = nativeRequire('mongoose');
const { createModels, createMethods, runAsSystem } = nativeRequire('@librechat/data-schemas');
const credentials = require('./credentials.cjs');
const recovery = require('./credential-recovery.cjs');
const core = require('./revocation.cjs');
const issuer = 'https://identity.example.test';
const user = '0123456789abcdef01234576';
const subject = 'dead-owner-subject';
const readyFile = '/fixture/dead-owner.ready';
let child;

async function main() {
  // The native session signer needs a disposable key in this independent process.
  process.env.JWT_REFRESH_SECRET = crypto.randomBytes(32).toString('hex');
  mongoose.set('autoIndex', false); mongoose.set('autoCreate', false);
  await mongoose.connect(process.env.MONGO_URI, { autoIndex: false, autoCreate: false, maxPoolSize: 1 });
  require('./writers.cjs').register(mongoose);
  const models = createModels(mongoose);
  const native = { ...models, ...createMethods(mongoose), mongoose, runAsSystem };
  if (process.argv[2] === '--writer') {
    const insert = models.Session.collection.insertOne;
    models.Session.collection.insertOne = async function(...args) {
      const result = await insert.apply(this, args);
      fs.writeFileSync(readyFile, 'inserted\n', { mode: 0o600, flag: 'wx' });
      await new Promise(() => {});
      return result;
    };
    await runAsSystem(() => native.createSession(user));
    throw new Error('Fixture writer unexpectedly completed');
  }
  const id = new mongoose.Types.ObjectId(user);
  await models.User.collection.insertOne({ _id: id, provider: 'openid', openidIssuer: issuer,
    openidId: subject, role: 'USER', name: 'Retained recovery user', email: 'dead-owner@example.test',
    tenantId: 'fixture-suite', [credentials.field]: {} });
  await runAsSystem(() => models.Conversation.create({ conversationId: 'fixture-dead-owner-chat',
    user, title: 'Retained through crash recovery', endpoint: 'openAI' }));
  const original = await models.User.collection.findOne({ _id: id });
  const chats = await models.Conversation.collection.find({ user }).toArray();
  const revoke = { contract: core.contract, action: 'revoke', user_id: user, issuer, subject };
  const check = { ...revoke, action: 'check' };
  child = spawn(process.execPath, [__filename, '--writer'], { stdio: 'ignore' });
  const exited = new Promise((resolve, reject) => {
    child.once('exit', (code, signal) => resolve({ code, signal })); child.once('error', reject);
  });
  for (let n = 0; n < 100 && !fs.existsSync(readyFile); n++) {
    if (child.exitCode !== null || child.signalCode !== null) { throw new Error('Owned writer stopped before insertion'); }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.equal(fs.readFileSync(readyFile, 'utf8'), 'inserted\n');
  assert.equal((await core.inspect(check, native, issuer)).credential_writes_in_flight, 1);
  await assert.rejects(core.revoke(revoke, native, issuer), /requires inspection/);
  child.kill('SIGKILL'); assert.equal((await exited).signal, 'SIGKILL'); child = null;
  const stopped = await models.User.collection.findOne({ _id: id });
  const claims = stopped[credentials.field];
  assert.equal(credentials.count(claims), 1);
  const sessions = await models.Session.collection.find({ user: id }).toArray();
  assert.equal(sessions.length, 1);
  const request = { contract: recovery.contract, action: 'recover', user_id: user, issuer, subject,
    all_writers_confirmed_offline: true, claim_digest: recovery.digest(claims), operation_id: crypto.randomUUID() };
  const noChange = async bad => {
    await assert.rejects(recovery.recover(bad, native, issuer), /recovery refused/);
    assert.deepEqual(await models.User.collection.findOne({ _id: id }), stopped);
  };
  await noChange({ ...request, all_writers_confirmed_offline: false });
  await noChange({ ...request, claim_digest: '0'.repeat(64) });
  await noChange({ ...request, issuer: 'https://foreign.example.test' });
  await noChange({ ...request, subject: 'foreign-subject' });
  await noChange({ ...request, operation_id: 'not-a-uuid' });
  await noChange({ ...request, extra: true });
  const update = models.User.collection.updateOne;
  const laterId = crypto.randomUUID();
  models.User.collection.updateOne = async function(...args) {
    await update.call(this, { _id: id }, { $set: { [credentials.field + '.' + laterId]: Object.values(claims)[0] } });
    return update.apply(this, args);
  };
  try { await assert.rejects(recovery.recover(request, native, issuer), /requires inspection/); }
  finally { models.User.collection.updateOne = update; }
  const changed = await models.User.collection.findOne({ _id: id });
  assert.equal(credentials.count(changed[credentials.field]), 2);
  assert.equal(Object.hasOwn(changed, credentials.recoveryField), false);
  // Fixture alone removes its injected CAS-race claim after the known writer is dead.
  await update.call(models.User.collection, { _id: id }, { $unset: { [credentials.field + '.' + laterId]: '' } });
  models.User.collection.updateOne = async function(...args) {
    await update.apply(this, args); throw new Error('Owned fixture lost recovery reply');
  };
  try { await assert.rejects(recovery.recover(request, native, issuer), /requires inspection/); }
  finally { models.User.collection.updateOne = update; }
  const recovered = await models.User.collection.findOne({ _id: id });
  assert.equal(credentials.count(recovered[credentials.field]), 0);
  assert.equal(recovered[credentials.recoveryField].length, 1);
  const report = await recovery.recover(request, native, issuer);
  assert.equal(report.reused, true); assert.equal(report.operation_id, request.operation_id);
  assert.equal(report.cleared_claims, 1); assert.equal(report.credential_writes_in_flight, 0);
  assert.deepEqual(await models.User.collection.findOne({ _id: id }), recovered);
  assert.deepEqual(await models.Session.collection.find({ user: id }).toArray(), sessions);
  const profile = { ...recovered }; delete profile[credentials.recoveryField];
  assert.deepEqual(profile, original);
  await assert.rejects(recovery.recover({ ...request, claim_digest: '0'.repeat(64) }, native, issuer), /recovery refused/);
  await assert.rejects(recovery.recover({ ...request, operation_id: crypto.randomUUID() }, native, issuer), /recovery refused/);
  // Restart an actual writer process, then kill it at the same native insert boundary.
  fs.unlinkSync(readyFile);
  child = spawn(process.execPath, [__filename, '--writer'], { stdio: 'ignore' });
  const restartedExit = new Promise((resolve, reject) => {
    child.once('exit', (code, signal) => resolve({ code, signal })); child.once('error', reject);
  });
  for (let n = 0; n < 100 && !fs.existsSync(readyFile); n++) {
    if (child.exitCode !== null || child.signalCode !== null) { throw new Error('Restarted writer stopped before insertion'); }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.equal(fs.readFileSync(readyFile, 'utf8'), 'inserted\n');
  child.kill('SIGKILL'); assert.equal((await restartedExit).signal, 'SIGKILL'); child = null;
  const restarted = await models.User.collection.findOne({ _id: id });
  const newerClaims = restarted[credentials.field];
  assert.equal(credentials.count(newerClaims), 1);
  assert.notEqual(Object.values(newerClaims)[0].generation, Object.values(claims)[0].generation);
  // A repeated old receipt must never clear the restarted writer's claim state.
  assert.equal((await recovery.recover(request, native, issuer)).credential_writes_in_flight, 1);
  assert.deepEqual((await models.User.collection.findOne({ _id: id }))[credentials.field], newerClaims);
  const restartRequest = { ...request, claim_digest: recovery.digest(newerClaims), operation_id: crypto.randomUUID() };
  for (const invalidHistory of [
    [{ ...recovered[credentials.recoveryField][0], confirmed_offline: false }],
    Array.from({ length: 64 }, () => ({ ...recovered[credentials.recoveryField][0], operation_id: crypto.randomUUID() })),
  ]) {
    await update.call(models.User.collection, { _id: id }, { $set: { [credentials.recoveryField]: invalidHistory } });
    const held = await models.User.collection.findOne({ _id: id });
    await assert.rejects(recovery.recover(restartRequest, native, issuer), /recovery refused/);
    assert.deepEqual(await models.User.collection.findOne({ _id: id }), held);
  }
  await update.call(models.User.collection, { _id: id }, { $set: { [credentials.recoveryField]: recovered[credentials.recoveryField] } });
  const freshReport = await recovery.recover(restartRequest, native, issuer);
  assert.equal(freshReport.reused, false); assert.equal(freshReport.cleared_claims, 1);
  assert.equal(freshReport.credential_writes_in_flight, 0);
  const restartedRecovered = await models.User.collection.findOne({ _id: id });
  assert.equal(restartedRecovered[credentials.recoveryField].length, 2);
  assert.equal(await models.Session.collection.countDocuments({ user: id }), 2);
  await runAsSystem(async () => {
    await assert.rejects(models.User.updateOne({ _id: id }, { $unset: { [credentials.recoveryField]: '' } }));
    assert.equal((await models.User.replaceOne({ _id: id }, { provider: 'local' })).matchedCount, 0);
    assert.equal((await models.User.deleteOne({ _id: id })).deletedCount, 0);
    assert.equal((await models.User.updateOne({ _id: id }, { $set: { name: 'Permitted profile update' } })).matchedCount, 1);
    await models.User.updateOne({ _id: id }, { $set: { name: original.name } });
  });
  const beforeRevoke = await models.User.collection.findOne({ _id: id });
  const proof = await core.revoke(revoke, native, issuer);
  assert.equal(proof.session_and_agent_keys_cleared, true);
  const final = await models.User.collection.findOne({ _id: id });
  assert.deepEqual(final[credentials.recoveryField], restartedRecovered[credentials.recoveryField]);
  delete final.kyquickstartRevocation;
  assert.deepEqual(final, beforeRevoke);
  assert.deepEqual(await models.Conversation.collection.find({ user }).toArray(), chats);
  console.log('PASS: actual killed native credential writer leaves a blocking claim; confirmed-offline exact-state recovery atomically audits clearance, refuses stale/source/unconfirmed requests, inspects lost replies and preserves profile/content/credentials until normal revocation cleanup.');
  console.log('LIMIT: internal offline maintenance primitive; no public command, replica-stop orchestration, authenticated administrator workflow, restore qualification or production release.');
}
main().then(() => mongoose.disconnect()).catch(async error => {
  if (child) { child.kill('SIGKILL'); }
  await mongoose.disconnect(); process.stderr.write('LibreChat native credential recovery qualification failed\n');
  // Source locations only: assertion values may contain native credential material.
  for (const line of String(error.stack).split('\n').filter(line => /^\s+at /.test(line) && line.includes('/module/')).slice(0, 3)) {
    process.stderr.write(line + '\n');
  }
  process.exitCode = 1;
});
