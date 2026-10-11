'use strict';
// Runs only against the disposable parent-owned MongoDB fixture.
const assert = require('node:assert/strict');
const { createRequire } = require('node:module');
const nativeRequire = createRequire('/app/package.json');
const mongoose = nativeRequire('mongoose');
const { createModels, createMethods, runAsSystem } = nativeRequire('@librechat/data-schemas');
const core = require('./revocation.cjs');
mongoose.set('autoIndex', false); mongoose.set('autoCreate', false);
const issuer = 'https://identity.example.test';
const request = { contract: core.contract, action: 'revoke', user_id: '0123456789abcdef01234567',
  issuer, subject: 'subject-雪' };
const owner = '0123456789abcdef01234568';

async function main() {
  await mongoose.connect(process.env.MONGO_URI, { autoIndex: false, autoCreate: false, maxPoolSize: 1 });
  require('./writers.cjs').register(mongoose);
  const models = createModels(mongoose);
  const native = { ...models, ...createMethods(mongoose), mongoose, runAsSystem };
  const jwt = await require('./jwt.native.cjs')(native);
  try {
  const oidcDaily = jwt.signOidc({ sub: request.subject, email: 'daily@example.test' });
  const oidcOwner = jwt.signOidc({ sub: 'owner-subject', email: 'owner@example.test' });
  const machineDaily = jwt.signOidc({ sub: 'daily-machine', client_id: 'daily-machine' });
  const machineOwner = jwt.signOidc({ sub: 'owner-machine', client_id: 'owner-machine' });
  assert.equal(await jwt.oidcAccess(oidcDaily), 200);
  assert.equal(await jwt.oidcAccess(oidcOwner), 200);
  assert.equal(await jwt.machineAccess(machineDaily), 200);
  assert.equal(await jwt.machineAccess(machineOwner), 200);
  assert.equal(await jwt.oidcAccess(oidcDaily.slice(0, -4) + 'xxxx'), 401);
  assert.equal(await jwt.machineAccess(machineDaily.slice(0, -4) + 'xxxx'), 401);
  assert.equal(await jwt.machineAccess(jwt.signOidc({ sub: 'daily-machine', client_id: 'daily-machine' }, { expiresIn: -1 })), 401);
  assert.equal(await jwt.machineAccess(jwt.signOidc({ sub: 'unknown-machine', client_id: 'unknown-machine' })), 401);
  assert.equal(await jwt.oidcAccess(jwt.signOidc({ sub: request.subject }, { audience: 'foreign' })), 401);
  assert.equal(await jwt.oidcAccess(jwt.signOidc({ sub: request.subject }, { issuer: 'https://foreign.example.test' })), 401);
  assert.equal(await jwt.oidcAccess(jwt.signOidc({ sub: request.subject, scope: 'foreign' })), 401);
  assert.equal(await jwt.machineAccess(jwt.signOidc({ sub: 'foreign', client_id: 'daily-machine' })), 401);
  assert.equal(await jwt.machineAccess(jwt.signOidc({ sub: 'daily-machine', client_id: 'daily-machine', azp: 'foreign' })), 401);
  const oldToken = jwt.sign(request.user_id);
  assert.equal(await jwt.authenticate(oldToken), request.user_id);
  assert.equal(await jwt.authenticate(jwt.sign(owner)), owner);
  assert.equal(await jwt.authenticate(oldToken.slice(0, -4) + 'xxxx'), null);
  const dailyTokens = await jwt.issue(new mongoose.Types.ObjectId(request.user_id));
  const ownerTokens = await jwt.issue(owner);
  assert.equal(await jwt.authenticate(dailyTokens.access), request.user_id);
  assert.equal(await jwt.bearerAccess(dailyTokens.access), 200);
  assert.equal(await jwt.imageAccess(dailyTokens.refresh, request.user_id), 200);
  assert.equal(await jwt.imageAccess(ownerTokens.refresh, owner), 200);
  assert.equal(await jwt.imageAccess(undefined, request.user_id), 401);
  assert.equal(await jwt.cookieAccess(dailyTokens.refresh), 200);
  assert.equal(await jwt.cookieAccess(ownerTokens.refresh), 200);
  assert.equal(await jwt.cookieAccess(), 401);
  assert.equal(await jwt.cookieAccess(dailyTokens.refresh.slice(0, -4) + 'xxxx'), 401);
  const id = new mongoose.Types.ObjectId(request.user_id);
  const original = await models.User.collection.findOne({ _id: id });
  const queuedDocument = await runAsSystem(() => models.User.findById(id));
  await runAsSystem(() => models.Session.create([
    { user: id, refreshTokenHash: 'fixture-expired', expiration: new Date(0) },
  ]));
  const dailyKey = await runAsSystem(() => native.createAgentApiKey({ userId: id, name: 'fixture-daily' }));
  const ownerKey = await runAsSystem(() => native.createAgentApiKey({ userId: owner, name: 'fixture-owner' }));
  assert.equal(await jwt.agentAccess(dailyKey.key), 200);
  assert.equal(await jwt.agentAccess(ownerKey.key), 200);
  assert.equal(await jwt.agentAccess(), 401);
  assert.equal(await jwt.agentAccess('sk-invalid-fixture-key'), 401);
  await runAsSystem(() => models.Conversation.create({
    conversationId: 'fixture-retained-conversation', user: request.user_id, title: 'Retained chat 雪', endpoint: 'openAI',
  }));
  const content = await models.Conversation.collection.find({ user: request.user_id }).toArray();
  await core.assertAccountActive(request.user_id, native);
  await core.assertSubjectActive(issuer, request.subject, native);
  const check = { ...request, action: 'check' };
  const before = await core.inspect(check, native, issuer);
  assert.equal(before.fence_published, false);
  assert.equal(before.active_sessions_remaining, 1);
  assert.equal(before.agent_keys_remaining, 1);
  assert.equal(before.credential_writes_in_flight, 0);
  for (const [model, create, nativeId] of [
    [models.Session, () => native.createSession(request.user_id), value => value.session._id],
    [models.AgentApiKey, () => native.createAgentApiKey({ userId: id, name: 'held-fixture-key' }), value => new mongoose.Types.ObjectId(value.id)],
  ]) {
    let entered; const admission = new Promise(resolve => { entered = resolve; });
    let finish; const gate = new Promise(resolve => { finish = resolve; });
    const insert = model.collection.insertOne;
    model.collection.insertOne = async function(...args) { const result = await insert.apply(this, args); entered(); await gate; return result; };
    const creating = runAsSystem(create);
    try {
      await admission;
      const held = await core.inspect(check, native, issuer);
      assert.equal(held.credential_writes_in_flight, 1); assert.equal(held.fence_published, false);
      await assert.rejects(core.revoke(request, native, issuer), /requires inspection/);
      assert.equal((await core.inspect(check, native, issuer)).fence_published, false);
      assert.equal(await runAsSystem(() => native.updateUser(request.user_id, { name: 'Forbidden during admission' })), null);
    } finally { finish(); model.collection.insertOne = insert; }
    const created = await creating;
    assert.equal((await core.inspect(check, native, issuer)).credential_writes_in_flight, 0);
    await model.collection.deleteOne({ _id: nativeId(created) });
    assert.deepEqual(await models.User.collection.findOne({ _id: id }), original);
  }
  const writerField = require('./credentials.cjs').field;
  const sessionRows = await models.Session.collection.find({ user: id }).sort({ _id: 1 }).toArray();
  const nativeInsert = models.Session.collection.insertOne;
  models.Session.collection.insertOne = async () => { throw new Error('Owned fixture insert failure'); };
  try { await assert.rejects(runAsSystem(() => native.createSession(request.user_id))); }
  finally { models.Session.collection.insertOne = nativeInsert; }
  assert.equal((await core.inspect(check, native, issuer)).credential_writes_in_flight, 0);
  assert.deepEqual(await models.Session.collection.find({ user: id }).sort({ _id: 1 }).toArray(), sessionRows);
  const userUpdate = models.User.collection.updateOne;
  models.User.collection.updateOne = async function(...args) {
    const result = await userUpdate.apply(this, args);
    if (Object.keys(args[1].$set || {}).some(key => key.startsWith(writerField + '.'))) { throw new Error('Lost owned admission reply'); }
    return result;
  };
  try { await assert.rejects(runAsSystem(() => native.createSession(request.user_id))); }
  finally { models.User.collection.updateOne = userUpdate; }
  assert.equal((await core.inspect(check, native, issuer)).credential_writes_in_flight, 1);
  await assert.rejects(core.revoke(request, native, issuer), /requires inspection/);
  assert.deepEqual(await models.Session.collection.find({ user: id }).sort({ _id: 1 }).toArray(), sessionRows);
  // Only this known fixture caller has terminated without insertion. Clear its
  // exact injected claim; production must never infer a dead owner from age.
  const claims = (await models.User.collection.findOne({ _id: id }))[writerField];
  assert.equal(Object.keys(claims).length, 1);
  await models.User.collection.updateOne({ _id: id }, { $unset: { [writerField + '.' + Object.keys(claims)[0]]: '' } });
  models.User.collection.updateOne = async function(...args) {
    const result = await userUpdate.apply(this, args);
    if (Object.keys(args[1].$unset || {}).some(key => key.startsWith(writerField + '.'))) { throw new Error('Lost owned release reply'); }
    return result;
  };
  try { await assert.rejects(runAsSystem(() => native.createSession(request.user_id))); }
  finally { models.User.collection.updateOne = userUpdate; }
  const afterRelease = await core.inspect(check, native, issuer);
  assert.equal(afterRelease.credential_writes_in_flight, 0);
  assert.equal(afterRelease.active_sessions_remaining, before.active_sessions_remaining + 1);
  const previousIds = new Set(sessionRows.map(row => String(row._id)));
  const unknownRows = (await models.Session.collection.find({ user: id }).toArray()).filter(row => !previousIds.has(String(row._id)));
  assert.equal(unknownRows.length, 1); await models.Session.collection.deleteOne({ _id: unknownRows[0]._id });
  await models.User.collection.updateOne({ _id: id }, { $set: { [writerField]: { corrupt: {} } } });
  await assert.rejects(core.inspect(check, native, issuer));
  await assert.rejects(runAsSystem(() => native.createSession(request.user_id)));
  await models.User.collection.updateOne({ _id: id }, { $set: { [writerField]: {} } });
  assert.deepEqual(await models.User.collection.findOne({ _id: id }), original);
  await assert.rejects(core.revoke({ ...request, subject: 'foreign' }, native, issuer));
  await assert.rejects(core.revoke({ ...request, action: 'check' }, native, issuer));
  await assert.rejects(core.revoke({ ...request, unknown: true }, native, issuer));
  assert.deepEqual(await models.User.collection.findOne({ _id: id }), original);
  const duplicateId = new mongoose.Types.ObjectId('0123456789abcdef01234570');
  await runAsSystem(() => models.User.create({ _id: duplicateId, email: 'collision@example.test',
    emailVerified: true, provider: 'openid', openidId: request.subject, openidIssuer: issuer, role: 'USER' }));
  await assert.rejects(core.revoke(request, native, issuer));
  assert.deepEqual(await models.User.collection.findOne({ _id: id }), original);
  await models.User.collection.deleteOne({ _id: duplicateId });
  await models.User.collection.updateOne({ _id: id }, { $set: { agentTriggerDeletionStartedAt: new Date() } });
  await assert.rejects(core.revoke(request, native, issuer), /requires inspection/);
  assert.equal((await core.inspect(check, native, issuer)).fence_published, false);
  await models.User.collection.updateOne({ _id: id }, { $unset: { agentTriggerDeletionStartedAt: '' } });

  // Actual commit with a lost reply. The published audit/fence must survive and
  // no cleanup may start until the unknown publication can be inspected.
  const update = models.User.collection.updateOne;
  models.User.collection.updateOne = async function(...args) {
    await update.apply(this, args); throw new Error('lost native publication reply');
  };
  try { await assert.rejects(core.revoke(request, native, issuer), /requires inspection/); }
  finally { models.User.collection.updateOne = update; }
  const held = await core.inspect(check, native, issuer);
  assert.equal(held.fence_published, true); assert.equal(held.session_and_agent_keys_cleared, false);
  assert.equal(held.active_sessions_remaining, 1); assert.equal(held.agent_keys_remaining, 1);
  await assert.rejects(runAsSystem(() => native.createSession(request.user_id)));
  await assert.rejects(runAsSystem(() => native.createAgentApiKey({ userId: id, name: 'forbidden-after-publication' })));
  assert.equal((await core.inspect(check, native, issuer)).credential_writes_in_flight, 0);
  assert.equal(await jwt.oidcAccess(oidcDaily), 401);
  assert.equal(await jwt.oidcAccess(jwt.signOidc({ sub: request.subject, email: 'daily@example.test' })), 401);
  assert.equal(await jwt.machineAccess(machineDaily), 401);
  assert.equal(await jwt.machineAccess(jwt.signOidc({ sub: 'daily-machine', client_id: 'daily-machine' })), 401);
  assert.equal(await jwt.oidcAccess(oidcOwner), 200);
  assert.equal(await jwt.machineAccess(machineOwner), 200);
  assert.equal(await jwt.authenticate(oldToken), null);
  assert.equal(await jwt.authenticate(jwt.sign(request.user_id)), null);
  assert.equal(await jwt.authenticate(jwt.sign(owner)), owner);
  // Publication must deny cookie authentication before credential cleanup starts.
  assert.equal(await jwt.cookieAccess(dailyTokens.refresh), 401);
  assert.equal(await jwt.bearerAccess(dailyTokens.access), 401);
  assert.equal(await jwt.bearerAccess(ownerTokens.access), 200);
  assert.equal(await jwt.imageAccess(dailyTokens.refresh, request.user_id), 403);
  assert.equal(await jwt.imageAccess(ownerTokens.refresh, owner), 200);
  assert.equal(await jwt.cookieAccess(ownerTokens.refresh), 200);
  assert.equal(await jwt.agentAccess(dailyKey.key), 401);
  assert.equal(await jwt.agentAccess(ownerKey.key), 200);
  assert.equal(await models.AgentApiKey.collection.countDocuments({ userId: id }), 1);
  const sessions = await models.Session.collection.find({}).sort({ _id: 1 }).toArray();
  await assert.rejects(jwt.issue(new mongoose.Types.ObjectId(request.user_id)), /Account access refused/);
  assert.deepEqual(await models.Session.collection.find({}).sort({ _id: 1 }).toArray(), sessions);
  await assert.rejects(core.assertAccountActive(request.user_id, native));
  await assert.rejects(core.assertSubjectActive(issuer, request.subject, native));
  await core.assertAccountActive(owner, native);
  await core.assertSubjectActive(issuer, 'owner-subject', native);

  const removeSessions = native.deleteAllUserSessions;
  native.deleteAllUserSessions = async (...args) => {
    await removeSessions(...args); throw new Error('lost native cleanup reply');
  };
  try { await assert.rejects(core.revoke(request, native, issuer), /requires inspection/); }
  finally { native.deleteAllUserSessions = removeSessions; }
  const partial = await core.inspect(check, native, issuer);
  assert.equal(partial.operation_id, held.operation_id);
  assert.equal(partial.active_sessions_remaining, 0); assert.equal(partial.agent_keys_remaining, 1);
  const done = await core.revoke(request, native, issuer);
  assert.equal(done.session_and_agent_keys_cleared, true); assert.equal(done.operation_id, held.operation_id);
  assert.deepEqual(await core.revoke(request, native, issuer), done);
  assert.equal(await models.Session.collection.countDocuments({ user: id }), 0);
  assert.equal(await models.AgentApiKey.collection.countDocuments({ userId: id }), 0);
  assert.equal(await models.Session.collection.countDocuments({ user: new mongoose.Types.ObjectId(owner) }), 1);
  assert.equal(await models.AgentApiKey.collection.countDocuments({ userId: new mongoose.Types.ObjectId(owner) }), 1);
  assert.equal(await jwt.oidcAccess(oidcDaily), 401);
  assert.equal(await jwt.machineAccess(machineDaily), 401);
  assert.equal(await jwt.agentAccess(dailyKey.key), 401);
  assert.equal(await jwt.agentAccess(ownerKey.key), 200);
  const retained = await models.User.collection.findOne({ _id: id });
  await runAsSystem(async () => {
    assert.equal(await native.updateUser(request.user_id, { name: 'Forbidden update' }), null);
    assert.equal((await native.deleteUserById(request.user_id)).deletedCount, 0);
    assert.equal(await models.User.findByIdAndDelete(id), null);
    assert.equal(await models.User.findOneAndDelete({ _id: id }), null);
    assert.equal((await models.User.replaceOne({ _id: id }, { provider: 'local', email: 'replacement@example.test' })).matchedCount, 0);
    assert.equal((await models.User.updateMany({ _id: id }, { $set: { openidId: 'foreign' } })).matchedCount, 0);
    assert.equal((await models.User.bulkWrite([{ updateOne: { filter: { _id: id }, update: { $set: { name: 'Forbidden bulk' } } } }])).matchedCount, 0);
    const stale = queuedDocument; stale.name = 'Forbidden save';
    await assert.rejects(stale.save());
    assert.equal((await stale.deleteOne()).deletedCount, 0);
    await assert.rejects(models.User.updateOne({ _id: id }, { $unset: { kyquickstartRevocation: '' } }));
    await assert.rejects(models.User.insertMany([{ email: 'foreign-record@example.test', kyquickstartRevocation: {} }]));
    assert.equal(await native.beginAgentTriggerUserDeletion(request.user_id, new Date()), 'in_progress');
    const temporary = await models.User.create({ email: 'temporary-writer@example.test', provider: 'local', role: 'USER' });
    assert.equal((await native.updateUser(String(temporary._id), { name: 'Allowed control update' })).name, 'Allowed control update');
    assert.equal((await native.deleteUserById(String(temporary._id))).deletedCount, 1);
  });
  assert.deepEqual(await models.User.collection.findOne({ _id: id }), retained);
  const record = retained.kyquickstartRevocation;
  assert.equal(record.audit_action, 'offboard'); assert.equal(record.operation_id, held.operation_id);
  delete retained.kyquickstartRevocation; assert.deepEqual(retained, original);
  assert.deepEqual(await models.Conversation.collection.find({ user: request.user_id }).toArray(), content);
  await assert.rejects(core.inspect({ ...check, subject: 'foreign' }, native, issuer));
  const badRecord = { ...record, binding_digest: 'c'.repeat(64) };
  await models.User.collection.updateOne({ _id: id }, { $set: { kyquickstartRevocation: badRecord } });
  await assert.rejects(core.inspect(check, native, issuer));
  await assert.rejects(core.revoke(request, native, issuer), /requires inspection/);
  await assert.rejects(core.assertAccountActive(request.user_id, native));
  await assert.rejects(core.assertSubjectActive(issuer, request.subject, native));
  assert.deepEqual((await models.User.collection.findOne({ _id: id })).kyquickstartRevocation, badRecord);
  assert.equal(await jwt.authenticate(oldToken), null);
  assert.equal(await jwt.cookieAccess(dailyTokens.refresh), 401);
  await models.User.collection.updateOne({ _id: id }, { $set: { kyquickstartRevocation: record } });
  await models.User.collection.updateOne({ _id: id }, { $set: { [writerField]: { corrupt: {} } } });
  await assert.rejects(core.inspect(check, native, issuer));
  await assert.rejects(core.revoke(request, native, issuer), /requires inspection/);
  await models.User.collection.updateOne({ _id: id }, { $set: { [writerField]: {} } });
  const find = models.User.collection.findOne;
  models.User.collection.findOne = async () => { throw new Error('private database diagnostic'); };
  try {
    await assert.rejects(core.assertAccountActive(request.user_id, native), error => error.message === 'LibreChat native revocation refused');
    await assert.rejects(core.assertSubjectActive(issuer, request.subject, native), error => error.message === 'LibreChat native revocation refused');
    assert.equal(await require('./runtime.cjs').isAccountActive(owner), false);
  } finally { models.User.collection.findOne = find; }
  const getUser = native.getUserById;
  native.getUserById = async () => { throw new Error('private native lookup diagnostic'); };
  try {
    await assert.rejects(core.inspect(check, native, issuer), error => error.message === 'LibreChat native revocation refused');
    await assert.rejects(core.revoke(request, native, issuer), error => error.message === 'LibreChat native revocation refused');
  } finally { native.getUserById = getUser; }
  console.log('PASS: real native atomic account/subject record+audit, lost publication/cleanup replies, guard checks, repeat and scoped session/key cleanup retain native user/profile/role and chat content.');
  console.log('PASS: patched native Passport JWT strategy accepts valid tokens before revocation, rejects invalid signatures and old/fresh valid tokens after publication, and preserves another account access.');
  console.log('PASS: native token issuance accepts ObjectId/string IDs and real refresh cookies over isolated Express HTTP before publication; publication denies cookie identity and new sessions before cleanup while another account keeps access.');
  console.log('PASS: patched native remote-agent authentication uses real generated keys/hashes/native user methods over isolated HTTP; publication denies a retained valid key before cleanup and afterward while another account keeps access; missing/invalid keys refuse.');
  console.log('PASS: actual native HTTPS JWKS verification validates RS256 issuer/audience/scope and machine bindings; publication denies old/fresh signed remote-user and machine tokens while control accounts retain access.');
  console.log('PASS: native secure image cookie authorization and common Passport bearer middleware deny the recorded user before session cleanup and preserve control account access; native image owner lookup is retained.');
  console.log('PASS: native model writer hooks retain the published User under profile/identity, delete, replacement, bulk and stale document-save attempts; native writers cannot supply the publication field.');
  console.log('PASS: real session/key writes held after MongoDB insert block publication and User mutation until owned admission releases; publication then refuses new native credential creation before any insert.');
  console.log('PASS: failed insert releases admission; lost claim reply retains an inspectable blocking claim; lost release reply leaves inspectable native credential state; corrupt claims refuse. No automatic dead-owner recovery is implemented.');
  console.log('LIMIT: isolated native middleware HTTP with explicit fixture configuration and HTTPS JWKS, not KyIdentity browser OIDC or full LibreChat/agent authorization. Email/permission boundaries are unused refusals; browser hooks and writer/restore coordination remain unqualified.');
  } finally { await jwt.close(); }
  await mongoose.disconnect();
}
main().catch(async error => {
  console.error('disposable native revocation failed', error);
  await mongoose.disconnect();
  process.exitCode = 1;
});
