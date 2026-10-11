'use strict';
// Install before native model compilation. Publication alone bypasses model hooks
// through the product's raw collection; ordinary native writers stay conditional.
const installed = new WeakSet();
const field = 'kyquickstartRevocation';
const credentials = require('./credentials.cjs');
const refused = () => new Error('Protected account write refused');
const protectedPath = value => typeof value === 'string' && [field, credentials.field, credentials.recoveryField].some(name => value === name || value.startsWith(name + '.'));
function checkUpdate(update) {
  if (!update || typeof update !== 'object' || Array.isArray(update)) { throw refused(); }
  for (const [key, value] of Object.entries(update)) {
    if (protectedPath(key)) { throw refused(); }
    if (key.startsWith('$') && value && typeof value === 'object') {
      for (const [path, target] of Object.entries(value)) {
        if (protectedPath(path) || (key === '$rename' && protectedPath(target))) { throw refused(); }
      }
    }
  }
}
const restrict = filter => ({ $and: [filter || {}, { [field]: { $exists: false } }, credentials.empty()] });

function register(mongoose) {
  if (installed.has(mongoose)) { return; }
  if (mongoose.models.User) { throw refused(); }
  mongoose.plugin(schema => {
    credentials.register(schema, mongoose);
    if (!schema.path('openidId') || !schema.path('openidIssuer') || !schema.path('provider')) { return; }
    schema.add({ [field]: { type: mongoose.Schema.Types.Mixed, select: false, immutable: true } });
    schema.add({ [credentials.field]: { type: mongoose.Schema.Types.Mixed, select: false, immutable: true } });
    schema.add({ [credentials.recoveryField]: { type: mongoose.Schema.Types.Mixed, select: false, immutable: true } });
    for (const method of ['updateOne', 'updateMany', 'findOneAndUpdate', 'replaceOne', 'findOneAndReplace', 'deleteOne', 'deleteMany']) {
      schema.pre(method, { query: true, document: false }, function() {
        if (!method.startsWith('delete')) { checkUpdate(this.getUpdate()); }
        this.setQuery(restrict(this.getQuery()));
        if (method.startsWith('delete') || ['replaceOne', 'findOneAndReplace'].includes(method)) {
          this.setQuery({ $and: [this.getQuery(), { [credentials.recoveryField]: { $exists: false } }] });
        }
      });
    }
    schema.pre('save', function() {
      if (this.isModified(field) || this.isModified(credentials.field) || this.isModified(credentials.recoveryField)) { throw refused(); }
      if (!this.isNew) { this.$where = restrict(this.$where); }
    });
    schema.pre('insertMany', function(next, documents) {
      try { for (const document of documents) { checkUpdate(document); } next(); } catch (error) { next(error); }
    });
    schema.pre('bulkWrite', function(next, operations) {
      try {
        for (const operation of operations) {
          const entries = Object.entries(operation);
          if (entries.length !== 1) { throw refused(); }
          const [kind, spec] = entries[0];
          if (kind === 'insertOne') { checkUpdate(spec.document); continue; }
          if (['updateOne', 'updateMany', 'replaceOne'].includes(kind)) { checkUpdate(spec.update || spec.replacement); }
          else if (!['deleteOne', 'deleteMany'].includes(kind)) { throw refused(); }
          spec.filter = restrict(spec.filter);
          if (kind.startsWith('delete') || kind === 'replaceOne') {
            spec.filter = { $and: [spec.filter, { [credentials.recoveryField]: { $exists: false } }] };
          }
        }
        next();
      } catch (error) { next(error); }
    });
  });
  installed.add(mongoose);
}
module.exports = { register };
