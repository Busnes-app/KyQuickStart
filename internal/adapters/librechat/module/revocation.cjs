'use strict';
// Product-internal primitive. The public CLI remains inspection-only until native
// authentication/writer integration is qualified; this proof is not access denial.
const crypto = require('node:crypto');
const account = require('./account.cjs');
const credentials = require('./credentials.cjs');
const contract = 'ky.librechat-revocation/1';
const field = 'kyquickstartRevocation';
const refused = () => new Error('LibreChat native revocation refused');
const uncertain = () => new Error('LibreChat revocation outcome requires inspection');

function binding(request) {
  if (!request || typeof request !== 'object' || Array.isArray(request) ||
      Object.keys(request).sort().join(',') !== 'action,contract,issuer,subject,user_id' ||
      request.contract !== contract || !['check', 'revoke'].includes(request.action)) {
    throw refused();
  }
  return account.validate({ contract: account.contract, user_id: request.user_id,
    issuer: request.issuer, subject: request.subject });
}

function digest(issuer, subject) {
  return crypto.createHash('sha256').update(issuer + '\0' + subject).digest('hex');
}

function validateRecord(record, id, expectedDigest) {
  if (!record || typeof record !== 'object' || Array.isArray(record) ||
      Object.keys(record).sort().join(',') !== 'audit_action,binding_digest,contract,operation_id,recorded_at,user_id' ||
      record.contract !== contract || record.audit_action !== 'offboard' || record.user_id !== id ||
      typeof record.binding_digest !== 'string' || !/^[0-9a-f]{64}$/.test(record.binding_digest) ||
      (expectedDigest && record.binding_digest !== expectedDigest) ||
      typeof record.operation_id !== 'string' ||
      !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(record.operation_id) ||
      typeof record.recorded_at !== 'string' || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(record.recorded_at) ||
      !Number.isFinite(Date.parse(record.recorded_at)) || new Date(record.recorded_at).toISOString() !== record.recorded_at) {
    throw refused();
  }
  return record;
}

function nativeId(id, native) {
  if (typeof id !== 'string' || !/^[0-9a-f]{24}$/.test(id)) { throw refused(); }
  return new native.mongoose.Types.ObjectId(id);
}

async function inspect(request, native, configuredIssuer) {
  const b = binding(request);
  try {
    const identity = await account.inspect(b, native, configuredIssuer);
    const row = await native.runAsSystem(() => native.User.collection.findOne(
      { _id: nativeId(b.user_id, native), provider: 'openid', openidIssuer: b.issuer, openidId: b.subject },
      { projection: { [field]: 1, [credentials.field]: 1 } }));
    if (!row) { throw refused(); }
    const record = Object.hasOwn(row, field) ? validateRecord(row[field], b.user_id, identity.binding_digest) : null;
    const pending = credentials.count(row[credentials.field]);
    const [sessions, keys] = await native.runAsSystem(() => Promise.all([
      native.countActiveSessions(b.user_id), native.listAgentApiKeys(b.user_id),
    ]));
    if (!Number.isSafeInteger(sessions) || sessions < 0 || !Array.isArray(keys) || keys.length > 10000) {
      throw refused();
    }
    return { contract: 'ky.librechat-revocation-proof/1', module_version: account.version,
      user_id: b.user_id, binding_digest: identity.binding_digest, role: identity.role,
      fence_published: record !== null, session_and_agent_keys_cleared: record !== null && pending === 0 && sessions === 0 && keys.length === 0,
      credential_writes_in_flight: pending,
      active_sessions_remaining: sessions, agent_keys_remaining: keys.length,
      operation_id: record ? record.operation_id : null };
  } catch { throw refused(); }
}

async function revoke(request, native, configuredIssuer) {
  if (request?.action !== 'revoke') { throw refused(); }
  const b = binding(request);
  let identity;
  let other;
  const id = nativeId(b.user_id, native);
  const exact = { _id: id, provider: 'openid', openidIssuer: b.issuer, openidId: b.subject };
  try {
    identity = await account.inspect(b, native, configuredIssuer);
    other = await native.runAsSystem(() => native.User.collection.countDocuments({
      _id: { $ne: id }, provider: 'openid', openidIssuer: b.issuer, openidId: b.subject,
    }));
  } catch { throw refused(); }
  if (other !== 0) { throw refused(); }
  try {
    // One atomic native User update publishes the account+subject fence and its
    // immutable local audit together. No replica-set transaction is required.
    await native.runAsSystem(() => native.User.collection.updateOne(
      { ...exact, [field]: { $exists: false }, agentTriggerDeletionStartedAt: { $exists: false }, ...credentials.empty() },
      { $set: { [field]: { contract, audit_action: 'offboard', user_id: b.user_id,
        binding_digest: identity.binding_digest, operation_id: crypto.randomUUID(),
        recorded_at: new Date().toISOString() } }, $unset: { expiresAt: '' } },
      { writeConcern: { w: 'majority', j: true } }));
    const row = await native.runAsSystem(() => native.User.collection.findOne(exact,
      { projection: { [field]: 1, [credentials.field]: 1 } }));
    if (!row) { throw refused(); }
    validateRecord(row[field], b.user_id, identity.binding_digest);
    if (credentials.count(row[credentials.field]) !== 0) { throw uncertain(); }
    // Inspect the durable fence before deleting credentials; unknown publication
    // leaves the account held for inspection rather than a claimed rollback.
    await native.runAsSystem(() => native.deleteAllUserSessions({ userId: b.user_id }));
    await native.runAsSystem(() => native.deleteAllAgentApiKeys(b.user_id));
    return await inspect({ ...request, action: 'check' }, native, configuredIssuer);
  } catch { throw uncertain(); }
}

async function assertAccountActive(id, native) {
  try {
    const row = await native.runAsSystem(() => native.User.collection.findOne(
      { _id: nativeId(id, native) }, { projection: { [field]: 1 } }));
    if (!row) { throw refused(); }
    if (Object.hasOwn(row, field)) {
      validateRecord(row[field], id);
      throw refused();
    }
  } catch { throw refused(); }
}

async function assertSubjectActive(issuer, subject, native) {
  try {
    account.validate({ contract: account.contract, user_id: '000000000000000000000000', issuer, subject });
    const value = digest(issuer, subject);
    const row = await native.runAsSystem(() => native.User.collection.findOne({ $or: [
      { [field + '.binding_digest']: value },
      { provider: 'openid', openidIssuer: issuer, openidId: subject, [field]: { $exists: true } },
    ] }, { projection: { [field]: 1 } }));
    if (row) {
      validateRecord(row[field], row._id.toString(), value);
      throw refused();
    }
  } catch { throw refused(); }
}

module.exports = { contract, inspect, revoke, validateRecord, assertAccountActive, assertSubjectActive };
