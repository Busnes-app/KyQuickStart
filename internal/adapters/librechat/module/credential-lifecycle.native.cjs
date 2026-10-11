'use strict';
// Disposable actual native methods/MongoDB check; credential values never leave memory.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const { createRequire } = require('node:module');
const nativeRequire = createRequire('/app/package.json');
const mongoose = nativeRequire('mongoose');
const { createModels, createMethods, runAsSystem } = nativeRequire('@librechat/data-schemas');
const credentials = require('./credentials.cjs');
const core = require('./revocation.cjs');
const issuer = 'https://identity.example.test';
const daily = '0123456789abcdef01234577';
const control = '0123456789abcdef01234578';
const disabled = '0123456789abcdef01234579';
const request = user => ({ contract: core.contract, action: 'revoke', user_id: user, issuer, subject: 'lifecycle-' + user });

async function main() {
  process.env.JWT_REFRESH_SECRET = crypto.randomBytes(32).toString('hex');
  mongoose.set('autoIndex', false); mongoose.set('autoCreate', false);
  await mongoose.connect(process.env.MONGO_URI, { autoIndex: false, autoCreate: false, maxPoolSize: 1 });
  require('./writers.cjs').register(mongoose);
  const models = createModels(mongoose);
  const native = { ...models, ...createMethods(mongoose), mongoose, runAsSystem };
  const oid = value => new mongoose.Types.ObjectId(value);
  for (const user of [daily, control, disabled]) {
    await models.User.collection.insertOne({ _id: oid(user), provider: 'openid', openidIssuer: issuer,
      openidId: 'lifecycle-' + user, role: 'USER', name: 'Retained lifecycle account', tenantId: 'fixture-suite' });
  }
  await core.revoke(request(disabled), native, issuer);
  const pending = async user => (await core.inspect({ ...request(user), action: 'check' }, native, issuer)).credential_writes_in_flight;
  const exp = () => new Date(Date.now() + 3600000);
  const providerToken = crypto.randomBytes(32).toString('hex');
  const stored = await runAsSystem(() => native.upsertSession(daily, providerToken, { expiration: exp(), tenantId: 'fixture-suite' }));
  const again = await runAsSystem(() => native.upsertSession(daily, providerToken, { expiration: exp(), tenantId: 'fixture-suite' }));
  assert.equal(String(stored._id), String(again._id)); assert.equal(await pending(daily), 0);
  const local = await runAsSystem(() => native.createSession(daily));
  local.session.expiration = new Date(Date.now() + 7200000);
  const rotated = await runAsSystem(() => native.generateRefreshToken(local.session));
  assert.notEqual(rotated, local.refreshToken);
  assert.equal(await runAsSystem(() => native.findSession({ refreshToken: local.refreshToken })), null);
  assert.ok(await runAsSystem(() => native.findSession({ refreshToken: rotated })));
  await runAsSystem(() => native.updateExpiration(String(local.session._id), exp()));
  await runAsSystem(() => native.updateExpiration(local.session, exp()));
  const key = await runAsSystem(() => native.createAgentApiKey({ userId: daily, name: 'lifecycle-key' }));
  assert.equal(String((await runAsSystem(() => native.validateAgentApiKey(key.key))).userId), daily);
  assert.ok((await models.AgentApiKey.collection.findOne({ _id: oid(key.id) })).lastUsedAt);
  assert.equal(await pending(daily), 0);

  for (const [model, owner, id] of [[models.Session, 'user', local.session._id], [models.AgentApiKey, 'userId', oid(key.id)]]) {
    const before = await model.collection.findOne({ _id: id });
    const document = await runAsSystem(() => model.findById(id));
    document[owner] = oid(control);
    await assert.rejects(runAsSystem(() => document.save()));
    assert.deepEqual(await model.collection.findOne({ _id: id }), before);
    await assert.rejects(runAsSystem(() => model.updateOne({ _id: id }, { $set: { [owner]: oid(control) } })));
    await assert.rejects(runAsSystem(() => model.updateOne({ _id: id }, { $unset: { [owner]: '' } })));
    await assert.rejects(runAsSystem(() => model.updateOne({ _id: id }, { $rename: { [owner]: 'foreignOwner' } })));
    await assert.rejects(runAsSystem(() => model.insertMany([{ ...before, _id: new mongoose.Types.ObjectId() }])));
    await assert.rejects(runAsSystem(() => model.bulkWrite([{ updateOne: { filter: { _id: id }, update: { $set: { [owner]: oid(control) } } } }])));
    assert.deepEqual(await model.collection.findOne({ _id: id }), before);
    assert.equal(await pending(daily), 0); assert.equal(await pending(control), 0);
  }

  // A row appearing after the pre-read must not widen an admitted updateMany.
  const late = { _id: new mongoose.Types.ObjectId(), user: oid(disabled), refreshTokenHash: 'fixture-late-race', expiration: exp() };
  const updateMany = models.Session.collection.updateMany;
  models.Session.collection.updateMany = async function(...args) {
    await models.Session.collection.insertOne(late);
    assert.equal(await pending(daily), 1);
    await assert.rejects(core.revoke(request(daily), native, issuer), /requires inspection/);
    return updateMany.apply(this, args);
  };
  try {
    const result = await runAsSystem(() => models.Session.updateMany({ refreshTokenHash: { $in: [stored.refreshTokenHash, late.refreshTokenHash] } },
      { $set: { expiration: new Date(Date.now() + 10800000) } }));
    assert.equal(result.matchedCount, 1);
  } finally { models.Session.collection.updateMany = updateMany; }
  assert.deepEqual(await models.Session.collection.findOne({ _id: late._id }), late);
  await models.Session.collection.deleteOne({ _id: late._id });

  const findOneAndUpdate = models.Session.collection.findOneAndUpdate;
  models.Session.collection.findOneAndUpdate = async function(...args) {
    await models.Session.collection.insertOne(late); return findOneAndUpdate.apply(this, args);
  };
  let broadCreated;
  try {
    broadCreated = await runAsSystem(() => models.Session.findOneAndUpdate({ refreshTokenHash: late.refreshTokenHash },
      { $set: { user: oid(daily), expiration: exp() } }, { upsert: true, new: true }));
    assert.equal(String(broadCreated.user), daily); assert.notEqual(String(broadCreated._id), String(late._id));
  } finally { models.Session.collection.findOneAndUpdate = findOneAndUpdate; }
  assert.deepEqual(await models.Session.collection.findOne({ _id: late._id }), late);
  await models.Session.collection.deleteOne({ _id: late._id });
  await runAsSystem(() => native.deleteSession({ sessionId: String(broadCreated._id) }));
  assert.equal(await pending(daily), 0);
  const priorSessions = await models.Session.collection.find({ user: oid(daily) }).sort({ _id: 1 }).toArray();
  await assert.rejects(runAsSystem(() => models.Session.updateMany({ user: oid(daily) }, { $set: { expiration: exp() } })));
  assert.deepEqual(await models.Session.collection.find({ user: oid(daily) }).sort({ _id: 1 }).toArray(), priorSessions);
  assert.equal(await pending(daily), 0);

  // A native logout between inspection and upsert execution stays a deletion.
  let deletedDuringUpsert, driverUpsert, upsertRejected = false;
  models.Session.collection.findOneAndUpdate = async function(...args) {
    deletedDuringUpsert = (await runAsSystem(() => native.deleteSession({ sessionId: String(stored._id) }))).deletedCount;
    driverUpsert = args[2].upsert;
    return findOneAndUpdate.apply(this, args);
  };
  try { await runAsSystem(() => native.upsertSession(daily, providerToken, { expiration: exp() })); }
  catch { upsertRejected = true; }
  finally { models.Session.collection.findOneAndUpdate = findOneAndUpdate; }
  assert.equal(deletedDuringUpsert, 1);
  assert.equal(driverUpsert, false);
  assert.equal(upsertRejected, true);
  assert.equal(await models.Session.collection.countDocuments({ user: oid(daily), refreshTokenHash: stored.refreshTokenHash }), 0);
  assert.equal(await pending(daily), 0);
  await runAsSystem(() => native.upsertSession(daily, providerToken, { expiration: exp() }));

  const updateOne = models.Session.collection.updateOne;
  models.Session.collection.updateOne = async function(...args) {
    await models.Session.collection.insertOne(late); return updateOne.apply(this, args);
  };
  try {
    assert.equal((await runAsSystem(() => models.Session.updateOne({ _id: late._id }, { $set: { expiration: exp() } }))).matchedCount, 0);
  } finally { models.Session.collection.updateOne = updateOne; }
  assert.deepEqual(await models.Session.collection.findOne({ _id: late._id }), late);
  await models.Session.collection.deleteOne({ _id: late._id });

  // Even a privileged concurrent owner change cannot redirect an admitted save.
  const stale = await runAsSystem(() => models.Session.findById(local.session._id));
  const row = await models.Session.collection.findOne({ _id: stale._id });
  models.Session.collection.updateOne = async function(...args) {
    await updateOne.call(this, { _id: stale._id }, { $set: { user: oid(disabled) } });
    return updateOne.apply(this, args);
  };
  try { stale.expiration = exp(); await assert.rejects(runAsSystem(() => stale.save())); }
  finally { models.Session.collection.updateOne = updateOne; }
  const rebound = await models.Session.collection.findOne({ _id: stale._id });
  assert.deepEqual(rebound, { ...row, user: oid(disabled) });
  // Only the fixture restores its own deliberately injected raw ownership change.
  await updateOne.call(models.Session.collection, { _id: stale._id }, { $set: { user: oid(daily) } });
  assert.equal(await pending(daily), 0);
  const snapshot = async () => ({
    sessions: await models.Session.collection.find({ user: oid(daily) }).sort({ _id: 1 }).toArray(),
    keys: await models.AgentApiKey.collection.find({ userId: oid(daily) }).sort({ _id: 1 }).toArray(),
  });
  const before = await snapshot();
  // Publish while retaining native credential rows so denial is proved before cleanup.
  const cleanup = native.deleteAllUserSessions;
  native.deleteAllUserSessions = async () => { throw new Error('Owned fixture paused cleanup'); };
  try { await assert.rejects(core.revoke(request(daily), native, issuer), /requires inspection/); }
  finally { native.deleteAllUserSessions = cleanup; }
  assert.equal((await core.inspect({ ...request(daily), action: 'check' }, native, issuer)).fence_published, true);
  await assert.rejects(runAsSystem(() => native.upsertSession(daily, providerToken, { expiration: exp() })));
  await assert.rejects(runAsSystem(() => native.upsertSession(daily, crypto.randomBytes(32).toString('hex'), { expiration: exp() })));
  await assert.rejects(runAsSystem(() => native.updateExpiration(String(local.session._id), exp())));
  await assert.rejects(runAsSystem(() => native.generateRefreshToken(local.session)));
  assert.equal(await runAsSystem(() => native.validateAgentApiKey(key.key)), null);
  assert.deepEqual(await snapshot(), before); assert.equal(await pending(daily), 0);
  const controlSession = await runAsSystem(() => native.createSession(control));
  assert.ok(await runAsSystem(() => native.findSession({ refreshToken: controlSession.refreshToken })));
  assert.equal((await core.revoke(request(daily), native, issuer)).session_and_agent_keys_cleared, true);
  assert.equal(await models.Session.collection.countDocuments({ user: oid(control) }), 1);
  console.log('PASS: native provider session insert/repeat upsert, refresh rotation/expiry and API-key last-use writes use exact credential/owner admission; document/query rebinding and broad/no-match races refuse without cross-owner mutation; publication denies retained credential writes before cleanup and control accounts remain usable.');
  console.log('LIMIT: batch writes refuse; complete native controller flows, privileged raw maintenance, source/new-account races, restore and public setup/revoke/resident acceptance remain gates.');
}
main().then(() => mongoose.disconnect()).catch(async error => {
  await mongoose.disconnect(); process.stderr.write('LibreChat native credential lifecycle qualification failed\n');
  for (const line of String(error.stack).split('\n').filter(line => /^\s+at /.test(line) && line.includes('/module/')).slice(0, 3)) {
    process.stderr.write(line + '\n');
  }
  process.exitCode = 1;
});
