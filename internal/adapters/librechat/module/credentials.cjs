'use strict';
const crypto = require('node:crypto');
const field = 'kyquickstartCredentialWrites';
const recoveryField = 'kyquickstartCredentialRecovery';
const state = Symbol('owned credential write');
const generation = crypto.randomUUID();
const refused = () => new Error('Credential write outcome requires inspection');
const empty = () => ({ $or: [{ [field]: { $exists: false } }, { [field]: {} }] });

function count(value) {
  if (value === undefined) { return 0; }
  if (!value || typeof value !== 'object' || Array.isArray(value)) { throw refused(); }
  const entries = Object.entries(value);
  if (entries.length > 32) { throw refused(); }
  for (const [id, entry] of entries) {
    if (!/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(id) ||
        !entry || Object.keys(entry).sort().join(',') !== 'generation,model,pid,started_at' ||
        !['Session', 'AgentApiKey'].includes(entry.model) || !Number.isSafeInteger(entry.pid) || entry.pid < 1 ||
        !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(entry.generation) || typeof entry.started_at !== 'string' ||
        !Number.isFinite(Date.parse(entry.started_at)) || new Date(entry.started_at).toISOString() !== entry.started_at) { throw refused(); }
  }
  return entries.length;
}

function register(schema, mongoose) {
  const owner = schema.path('refreshTokenHash') && schema.path('expiration') && schema.path('user') ? 'user' :
    schema.path('keyHash') && schema.path('keyPrefix') && schema.path('userId') ? 'userId' : null;
  if (!owner) { return; }
  async function acquire(actor, user) {
    const id = String(user);
    if (!/^[0-9a-f]{24}$/.test(id)) { throw refused(); }
    const nonce = crypto.randomUUID();
    const lease = { id: new mongoose.Types.ObjectId(id), nonce };
    try {
      const row = await mongoose.models.User.collection.findOne({ _id: lease.id }, { projection: { [field]: 1 } });
      if (!row) { throw refused(); } count(row[field]);
      const result = await mongoose.models.User.collection.updateOne({ _id: lease.id,
        kyquickstartRevocation: { $exists: false }, agentTriggerDeletionStartedAt: { $exists: false },
        $expr: { $lt: [{ $size: { $objectToArray: { $ifNull: ['$' + field, {}] } } }, 32] } },
      { $set: { [field + '.' + nonce]: { generation, pid: process.pid, model: actor.constructor.modelName || actor.model.modelName,
        started_at: new Date().toISOString() } } }, { writeConcern: { w: 'majority', j: true } });
      if (result.matchedCount !== 1) { throw refused(); }
      actor[state] = lease;
    } catch { throw refused(); }
  }
  async function release(actor) {
    const lease = actor[state]; delete actor[state];
    if (!lease) { return; }
    try {
      const result = await mongoose.models.User.collection.updateOne({ _id: lease.id,
        [field + '.' + lease.nonce + '.generation']: generation }, { $unset: { [field + '.' + lease.nonce]: '' } },
      { writeConcern: { w: 'majority', j: true } });
      if (result.matchedCount !== 1) { throw refused(); }
    } catch { throw refused(); }
  }
  schema.pre('save', async function() {
    if (!this.isNew && this.isModified(owner)) { throw refused(); }
    await acquire(this, this[owner]);
    if (!this.isNew) { this.$where = { $and: [this.$where || {}, { [owner]: this[owner] }] }; }
  });
  schema.post('save', async function() { await release(this); });
  schema.post('save', function(error, document, next) { release(this).then(() => next(error), next); });
  for (const method of ['updateOne', 'updateMany', 'findOneAndUpdate', 'replaceOne', 'findOneAndReplace']) {
    schema.pre(method, async function() {
      const update = this.getUpdate();
      if (!update || typeof update !== 'object' || Array.isArray(update)) { throw refused(); }
      if (Object.hasOwn(update.$unset || {}, owner) || Object.hasOwn(update.$rename || {}, owner) ||
          Object.values(update.$rename || {}).includes(owner)) { throw refused(); }
      // Native APIs pass string IDs; raw collection reads do not apply schema casts.
      // Inspect exactly the typed filter that MongoDB will receive for this write.
      this.setQuery(this.cast(this.model));
      const documents = await this.model.collection.find(this.getQuery(), { projection: { [owner]: 1 } }).limit(2).toArray();
      if (documents.length > 1) { throw refused(); }
      const assigned = update[owner] ?? update.$set?.[owner] ?? update.$setOnInsert?.[owner];
      if (['replaceOne', 'findOneAndReplace'].includes(method) && assigned == null) { throw refused(); }
      if (documents.length === 0 && !this.getOptions().upsert) {
        // Preserve the inspected no-match result if a row appears before execution.
        this.setQuery({ $and: [this.getQuery(), { _id: { $in: [] } }] }); return;
      }
      const user = documents[0]?.[owner] ?? assigned ?? this.getQuery()[owner];
      if (assigned != null && String(assigned) !== String(user)) { throw refused(); }
      await acquire(this, user);
      // Never let a broad query mutate newly matching credentials of another owner.
      const original = this.getQuery();
      const ownerPinned = (typeof original[owner] === 'string' || original[owner] instanceof mongoose.Types.ObjectId) &&
        String(original[owner]) === String(user);
      // Duplicate equality paths prevent MongoDB from inferring an upsert document.
      this.setQuery({ $and: [original, ...(ownerPinned ? [] : [{ [owner]: user }]),
        ...(documents.length ? [{ _id: documents[0]._id }] : [])] });
      // A credential deleted after inspection must not be recreated by this write.
      if (documents.length) { this.setOptions({ upsert: false }); }
    });
    schema.post(method, async function() { await release(this); });
    schema.post(method, function(error, result, next) { release(this).then(() => next(error), next); });
  }
  // Native credential APIs save one document or update one owner. Batch maintenance
  // must acquire explicit ownership before it can become a supported writer.
  schema.pre('insertMany', function() { throw refused(); });
  schema.pre('bulkWrite', function() { throw refused(); });
}
module.exports = { field, recoveryField, empty, count, register };
