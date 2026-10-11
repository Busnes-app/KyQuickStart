'use strict';
// Product-internal maintenance only. The caller must stop ALL native writers,
// including other replicas and workers, before confirming offline operation.
const crypto = require('node:crypto');
const account = require('./account.cjs');
const core = require('./revocation.cjs');
const credentials = require('./credentials.cjs');
const contract = 'ky.librechat-credential-recovery/1';
const refused = () => new Error('LibreChat credential recovery refused');
const uncertain = () => new Error('LibreChat credential recovery outcome requires inspection');
const uuid = value => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value);
const hash = value => typeof value === 'string' && /^[0-9a-f]{64}$/.test(value);

function digest(claims) {
  credentials.count(claims);
  // Canonical validated tuples, independent of BSON/JSON property ordering.
  return crypto.createHash('sha256').update(JSON.stringify(Object.entries(claims || {})
    .sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([id, c]) => [id, c.generation, c.model, c.pid, c.started_at]))).digest('hex');
}

function history(value, bindingDigest) {
  if (value === undefined) { return []; }
  if (!Array.isArray(value) || value.length > 64) { throw refused(); }
  const ids = new Set();
  for (const row of value) {
    if (!row || Object.keys(row).sort().join(',') !== 'binding_digest,claim_digest,cleared_claims,confirmed_offline,operation_id,recorded_at' ||
        !uuid(row.operation_id) || ids.has(row.operation_id) || row.binding_digest !== bindingDigest || !hash(row.claim_digest) ||
        row.confirmed_offline !== true || !Number.isSafeInteger(row.cleared_claims) || row.cleared_claims < 1 || row.cleared_claims > 32 ||
        typeof row.recorded_at !== 'string' || !Number.isFinite(Date.parse(row.recorded_at)) ||
        new Date(row.recorded_at).toISOString() !== row.recorded_at) { throw refused(); }
    ids.add(row.operation_id);
  }
  return value;
}

async function recover(request, native, configuredIssuer) {
  if (!request || Object.keys(request).sort().join(',') !== 'action,all_writers_confirmed_offline,claim_digest,contract,issuer,operation_id,subject,user_id' ||
      request.contract !== contract || request.action !== 'recover' || request.all_writers_confirmed_offline !== true ||
      !uuid(request.operation_id) || !hash(request.claim_digest)) { throw refused(); }
  const binding = account.validate({ contract: account.contract, user_id: request.user_id, issuer: request.issuer, subject: request.subject });
  const exact = { _id: new native.mongoose.Types.ObjectId(binding.user_id), provider: 'openid',
    openidIssuer: binding.issuer, openidId: binding.subject };
  let row, identity, records, pending;
  try {
    identity = await account.inspect(binding, native, configuredIssuer);
    if (await native.runAsSystem(() => native.User.collection.countDocuments({ ...exact,
      _id: { $ne: exact._id } })) !== 0) { throw refused(); }
    row = await native.runAsSystem(() => native.User.collection.findOne(exact));
    if (!row || Object.hasOwn(row, 'agentTriggerDeletionStartedAt')) { throw refused(); }
    if (Object.hasOwn(row, 'kyquickstartRevocation')) {
      core.validateRecord(row.kyquickstartRevocation, binding.user_id, identity.binding_digest);
    }
    records = history(row[credentials.recoveryField], identity.binding_digest);
    pending = credentials.count(row[credentials.field]);
  } catch { throw refused(); }
  const report = (receipt, reused, remaining) => ({ contract: 'ky.librechat-credential-recovery-report/1',
    user_id: binding.user_id, binding_digest: identity.binding_digest, operation_id: receipt.operation_id,
    claim_digest: receipt.claim_digest, cleared_claims: receipt.cleared_claims,
    credential_writes_in_flight: remaining, reused });
  const previous = records.find(r => r.operation_id === request.operation_id);
  if (previous) {
    if (previous.claim_digest !== request.claim_digest) { throw refused(); }
    return report(previous, true, pending);
  }
  if (!pending || records.length >= 64 || digest(row[credentials.field]) !== request.claim_digest) { throw refused(); }
  const receipt = { operation_id: request.operation_id, binding_digest: identity.binding_digest,
    claim_digest: request.claim_digest, cleared_claims: pending, confirmed_offline: true, recorded_at: new Date().toISOString() };
  const saved = name => Object.hasOwn(row, name) ? row[name] : { $exists: false };
  try {
    const result = await native.runAsSystem(() => native.User.collection.updateOne({ ...exact,
      agentTriggerDeletionStartedAt: { $exists: false }, kyquickstartRevocation: saved('kyquickstartRevocation'),
      [credentials.field]: row[credentials.field], [credentials.recoveryField]: saved(credentials.recoveryField) },
    { $set: { [credentials.field]: {}, [credentials.recoveryField]: [...records, receipt] } },
    { writeConcern: { w: 'majority', j: true } }));
    if (result.matchedCount !== 1) { throw uncertain(); }
    return report(receipt, false, 0);
  } catch { throw uncertain(); }
}
module.exports = { contract, digest, recover };
