// Optional real bundled-client upgrade case. No product crypto is implemented here.
import assert from "node:assert/strict";
import { appendText, replaceContentText, focusParagraphEnd } from "./notes_input.mjs";
import { createHash } from "node:crypto";
import { readFile, writeFile, chmod } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import path from "node:path";

let input = "";
for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input);
input = "";
const { firefox } = await import(config.module);
const notebook = "Everyday migration notebook 漢字";
const title = "Private everyday page café";
const body = "Old image encrypted text ✓ 漢字 café";
const edited = "Candidate edited text ✓ retained client key";
let stage = "start";
let context;
let captureEditorProof;
let badWire = false;
let unexpectedDialog = false;
let confirmations = 0;
const diagnostics = { badResponses: [], failedRequests: [], pageErrors: [], loginProofs: [] };
const log = (next) => { stage = next; console.log(JSON.stringify({ at: new Date().toISOString(), stage, phase: config.phase })); };
const sha256 = bytes => createHash("sha256").update(bytes).digest("hex");
const allowed = new Set([new URL(config.notes).host, new URL(config.identity).host]);
const proxy = http.createServer((_req, res) => { res.writeHead(403).end(); });
const sockets = new Set();
proxy.on("connection", socket => { sockets.add(socket); socket.on("close", () => sockets.delete(socket)); });
proxy.on("connect", (req, client, head) => {
  if (!allowed.has(req.url)) { client.end("HTTP/1.1 403 Forbidden\r\n\r\n"); return; }
  const remote = net.connect({ host: config.nodeIP, port: 30443 }, () => {
    client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
    if (head.length) remote.write(head);
    client.pipe(remote); remote.pipe(client);
  });
  client.on("error", () => remote.destroy());
  remote.on("error", () => client.destroy());
  client.on("close", () => remote.destroy());
});
await new Promise(resolve => proxy.listen(0, "127.0.0.1", resolve));
const proxyPort = proxy.address().port;
let exitCode = 1;
try {
  log("launch");
  context = await firefox.launchPersistentContext(config.profile, {
    executablePath: config.browser, channel: "moz-firefox", headless: true,
    timeout: 30000, ignoreHTTPSErrors: false, viewport: { width: 1280, height: 900 },
    firefoxUserPrefs: {
      "network.proxy.type": 1, "network.proxy.ssl": "127.0.0.1", "network.proxy.ssl_port": proxyPort,
      "network.proxy.no_proxies_on": "", "dom.ipc.processCount": 1,
      "dom.ipc.processCount.webIsolated": 1, "fission.autostart": false,
      "remote.prefs.recommended": false, "browser.newtab.preload": false,
      "gfx.webrender.render-backend-thread-count": 1, "dom.ipc.processPrelaunch.enabled": false,
    },
  });
  // BiDi exposes no request body. Observe bounded copies of the native fetch
  // arguments without changing the request, headers, response or confirmation flow.
  await context.addInitScript(({ origin, markers }) => {
    const nativeFetch = window.fetch;
    const proof = { pending: 0, inspectedBodies: 0, uninspectableBodies: 0, plaintextSeen: false, records: [] };
    window.__kyqContentRequests = proof;
    window.fetch = function(resource, init) {
      try {
        const url = new URL(resource instanceof Request ? resource.url : String(resource), location.href);
        const method = String(init?.method ?? (resource instanceof Request ? resource.method : "GET")).toUpperCase();
        if (url.origin === origin && /^\/api\/v1\/(objects|containers)(?:\/|$)/.test(url.pathname) && !["GET", "HEAD"].includes(method)) {
          if (proof.records.length >= 128) proof.uninspectableBodies += 1;
          else {
            const record = { method, path: url.pathname, bytes: 0, sha256: "" };
            proof.records.push(record);
            const body = init?.body ?? (resource instanceof Request ? resource.clone().arrayBuffer() : null);
            let bytes;
            if (body == null) bytes = new Uint8Array();
            else if (typeof body === "string") bytes = new TextEncoder().encode(body);
            else if (body instanceof ArrayBuffer) bytes = new Uint8Array(body.slice(0));
            else if (ArrayBuffer.isView(body)) bytes = new Uint8Array(body.buffer, body.byteOffset, body.byteLength).slice();
            else if (body instanceof Blob) bytes = body.slice().arrayBuffer();
            else if (body instanceof URLSearchParams) bytes = new TextEncoder().encode(body.toString());
            else if (body instanceof Promise) bytes = body;
            else throw new Error("uninspectable body type");
            proof.pending += 1;
            Promise.resolve(bytes).then(async value => {
              const data = value instanceof Uint8Array ? value : new Uint8Array(value);
              if (data.byteLength > 1048576) throw new Error("observer body cap");
              const text = new TextDecoder().decode(data);
              if (markers.some(marker => text.includes(marker))) proof.plaintextSeen = true;
              record.bytes = data.byteLength;
              record.sha256 = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", data))).map(byte => byte.toString(16).padStart(2, "0")).join("");
              proof.inspectedBodies += 1;
            }).catch(() => { proof.uninspectableBodies += 1; }).finally(() => { proof.pending -= 1; });
          }
        }
      } catch { proof.uninspectableBodies += 1; }
      return Reflect.apply(nativeFetch, this, arguments);
    };
  }, { origin: config.notes, markers: [notebook, title, body, edited].flatMap(text => [text, text.split(/[^\x00-\x7F]/)[0].trim()]).filter(text => text.length > 8) });
  // Preserve the client keystore, but force fresh native sessions in both phases.
  await context.clearCookies();
  context.setDefaultTimeout(60000);
  const page = await context.newPage();
  const safeURL = raw => { const url = new URL(raw); return { origin: url.origin, route: url.pathname.split("/").slice(0, 4).join("/") }; };
  page.on("response", response => { if (response.status() >= 400 && diagnostics.badResponses.length < 20) diagnostics.badResponses.push({ status: response.status(), ...safeURL(response.url()) }); });
  page.on("requestfailed", request => { if (diagnostics.failedRequests.length < 20) diagnostics.failedRequests.push({ ...safeURL(request.url()), code: request.failure()?.errorText.match(/(?:NS_ERROR_[A-Z_]+|SSL_ERROR_[A-Z_]+|ERR_[A-Z_]+)/)?.[0] ?? "network failure" }); });
  page.on("pageerror", error => { diagnostics.pageErrors.push({ name: error.name, kind: error.message.match(/^(?:ReferenceError|TypeError|SyntaxError|Error)/)?.[0] ?? "script error" }); });
  page.on("dialog", async dialog => {
    if (dialog.type() === "prompt" && dialog.message() === "Notebook name") await dialog.accept(notebook);
    else { unexpectedDialog = true; await dialog.dismiss(); }
  });
  async function replaceText(target, control, text) {
    await control.click();
    await control.press("ControlOrMeta+A");
    await control.press("Backspace");
    await appendText(target, text);
    assert.equal(await control.inputValue(), text);
  }
  async function identityPassword(target) {
    log("identity-origin-redirect");
    await target.waitForURL(url => url.origin === config.identity, { timeout: 30000 });
    const username = target.getByLabel("Username", { exact: true });
    log("identity-username-control");
    await username.waitFor();
    const password = target.getByLabel("Password", { exact: true });
    await password.evaluate(input => {
      window.__kyqAuthEvents = { inputs: 0, trustedInputs: 0, submits: 0 };
      input.form.addEventListener("input", event => { window.__kyqAuthEvents.inputs += 1; if (event.isTrusted) window.__kyqAuthEvents.trustedInputs += 1; }, true);
      input.form.addEventListener("submit", () => { window.__kyqAuthEvents.submits += 1; }, true);
    });
    if (await username.isEditable()) {
      await replaceText(target, username, config.username);
    }
    assert.equal(await username.inputValue(), config.username);
    log("identity-password-control");
    await replaceText(target, password, config.password);
    assert.equal(await password.inputValue(), config.password);
    assert.equal(await password.evaluate(input => input.form.checkValidity()), true);
    const observed = target.waitForResponse(response => new URL(response.url()).origin === config.identity && new URL(response.url()).pathname === "/api/auth/login" && response.request().method() === "POST", { timeout: 30000 }).then(response => ({ response }), error => ({ error }));
    log("identity-signin-submit");
    await target.getByRole("button", { name: "Sign in", exact: true }).click();
    log("identity-login-response");
    const observation = await observed;
    if (observation.error) throw observation.error;
    const response = observation.response;
    assert.equal(response.request().postDataJSON(), null, "BiDi request-body capability changed; review observer coverage");
    const reply = await response.json();
    assert.ok(reply && typeof reply === "object");
    const proof = { status: response.status(), inputValuesMatchFixture: true, protocolRequestBodyAvailable: false, usernameMatchesFixture: reply.user?.username === config.username, success: reply.success === true, mfaRequired: reply.mfaRequired === true, userReturned: Boolean(reply.user), passwordChangeRequired: reply.user?.passwordChangeRequired === true, enrollmentRestricted: reply.user?.enrollment?.restricted === true, restartAuthorization: reply.restartAuthorization === true };
    diagnostics.loginProofs.push(proof);
    console.log(JSON.stringify({ at: new Date().toISOString(), stage: "identity-login-proof", phase: config.phase, ...proof }));
    assert.equal(proof.status, 200);
    assert.equal(proof.success, true);
    assert.equal(proof.usernameMatchesFixture, true);
    assert.equal(proof.mfaRequired, false);
    assert.equal(proof.userReturned, true);
    assert.equal(proof.passwordChangeRequired, false);
    assert.equal(proof.enrollmentRestricted, false);
    assert.equal(proof.restartAuthorization, false);
  }
  // Each protected action must complete the product's real action-bound confirmation.
  async function confirmAction(dialog) {
    const popupPromise = context.waitForEvent("page");
    await dialog.getByRole("button", { name: "Continue to KySignOn", exact: true }).click();
    const popup = await popupPromise;
    await identityPassword(popup);
    await dialog.waitFor({ state: "hidden" });
    confirmations += 1;
  }
  async function confirmed(promise) {
    let settled;
    promise.then(value => { settled = { value }; }, error => { settled = { error }; });
    const deadline = Date.now() + 90000;
    while (!settled && Date.now() < deadline) {
      const dialog = page.getByRole("dialog", { name: "Confirm this action", exact: true });
      if (await dialog.isVisible()) await confirmAction(dialog);
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    assert.ok(settled, "protected action did not finish");
    if (settled.error) throw settled.error;
    return settled.value;
  }
  log("native-oidc-signin");
  log("notes-login-navigation");
  await page.goto(config.notes + "/");
  log("notes-sso-link");
  await page.getByRole("link", { name: "Continue with single sign-on", exact: true }).click();
  await identityPassword(page);
  log("notes-native-callback");
  await page.waitForURL(url => url.origin === config.notes);
  log("notes-workspace-navigation");
  await page.locator("#notebook-navigation").waitFor({ state: "attached" });
  const saved = () => {
    const pending = page.waitForResponse(response => response.request().method() === "PUT" && /\/api\/v1\/objects\/obj_/.test(response.url()) && response.ok());
    // A click may fail first. Handle rejection now, while the caller still awaits the original promise.
    pending.catch(() => {});
    return pending;
  };
  const readState = async objectID => {
    const values = await page.evaluate(async id => {
      const identityResponse = await fetch("/api/v1/me/identity");
      const objectResponse = await fetch("/api/v1/objects/" + encodeURIComponent(id));
      if (!identityResponse.ok || !objectResponse.ok) throw new Error("native state read refused");
      return { identity: await identityResponse.json(), bytes: Array.from(new Uint8Array(await objectResponse.arrayBuffer())) };
    }, objectID);
    const identity = values.identity;
    assert.equal(typeof identity.deviceId, "string");
    assert.equal(typeof identity.publicKey, "string");
    assert.equal(typeof identity.recoveryId, "string");
    assert.ok(identity.recoveryId.length > 0);
    const bytes = Buffer.from(values.bytes);
    assert.ok(bytes.length > 0);
    assert.ok(![title, body, edited].some(text => bytes.includes(Buffer.from(text))));
    await page.waitForFunction(() => window.__kyqContentRequests?.pending === 0);
    const observed = await page.evaluate(() => window.__kyqContentRequests);
    assert.ok(observed);
    assert.equal(observed.uninspectableBodies, 0);
    assert.equal(observed.plaintextSeen, false);
    badWire ||= observed.plaintextSeen;
    const objectSHA256 = sha256(bytes);
    const lastWrite = observed.records.filter(record => record.method === "PUT" && record.path === "/api/v1/objects/" + objectID).at(-1);
    if (config.phase === "seed" || lastWrite) {
      assert.ok(lastWrite && lastWrite.bytes > 0);
      assert.equal(lastWrite.sha256, objectSHA256, "native fetch body differs from stored ciphertext");
    }
    console.log(JSON.stringify({ at: new Date().toISOString(), stage: "native-content-request-proof", phase: config.phase, inspectedBodies: observed.inspectedBodies, uninspectableBodies: 0, plaintextSeen: false, storedCiphertextMatchesLastWrite: Boolean(lastWrite) }));
    return { identity: { deviceId: identity.deviceId, publicKey: identity.publicKey, recoveryId: identity.recoveryId }, objectSHA256 };
  };
  captureEditorProof = async label => {
    const values = await page.locator(".bn-editor").evaluateAll(editors => Array.from(editors).slice(0, 4).map(editor => ({ text: editor.innerText.slice(0, 65536), raw: (editor.textContent ?? "").slice(0, 65536), paragraphs: editor.querySelectorAll("p").length })));
    const expected = body + " " + edited;
    const proofs = values.map(({ text, raw, paragraphs }) => ({ characters: text.length, sha256: sha256(text), rawCharacters: raw.length, rawSHA256: sha256(raw), paragraphs, matchesOld: text === body, matchesExpectedEdit: text === expected, containsOld: text.includes(body), containsEdit: text.includes(edited), expectedPrefix: expected.startsWith(text), expectedSuffix: expected.endsWith(text), matchesIfWhitespaceCollapsed: text.replace(/\s+/gu, " ").trim() === expected, whitespace: Array.from(text).flatMap((character, index) => /\s/u.test(character) ? [{ index, codePoint: character.codePointAt(0) }] : []).slice(0, 128) }));
    const proof = { at: new Date().toISOString(), stage: "editor-content-proof", point: label, phase: config.phase, editorCount: values.length, expectedCharacters: expected.length, expectedSHA256: sha256(expected), editors: proofs };
    diagnostics.editorProofs ??= [];
    if (diagnostics.editorProofs.length < 8) diagnostics.editorProofs.push(proof);
    console.log(JSON.stringify(proof));
  };
  async function flushPage() {
    const flushed = saved();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await confirmed(flushed);
    await page.waitForFunction(() => document.querySelector(".save-state .ky-status")?.textContent === "Saved to server" && !document.querySelector(".save-detail")?.textContent.includes("Autosaving"));
  }
  async function dropOwnedContentCache() {
    // No app scripts run on healthz. Remove only this throwaway profile's content cache,
    // after proving its queues empty; retained identity/key records must remain byte-identical.
    await page.goto(config.notes + "/healthz");
    const proof = await page.evaluate(async () => {
      const request = indexedDB.open("kynotes-web");
      const db = await new Promise((resolve, reject) => { request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error); request.onupgradeneeded = () => { request.transaction.abort(); reject(new Error("expected existing client database")); }; });
      try {
        if (db.version !== 7 || !["notes", "pending", "uploads", "keys"].every(name => db.objectStoreNames.contains(name))) throw new Error("unexpected client cache schema");
        const all = name => new Promise((resolve, reject) => { const req = db.transaction(name).objectStore(name).getAll(); req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error); });
        const digest = async rows => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify(rows, (_key, value) => value instanceof Uint8Array ? Array.from(value) : value))))).map(byte => byte.toString(16).padStart(2, "0")).join("");
        const pending = await all("pending"), uploads = await all("uploads"), keys = await all("keys");
        if (pending.length || uploads.length || !keys.length) throw new Error("unsent data or missing retained client keys");
        const keySHA256 = await digest(keys);
        const notes = await all("notes");
        if (!notes.length) throw new Error("expected real cached content before eviction");
        await new Promise((resolve, reject) => { const tx = db.transaction("notes", "readwrite"); tx.objectStore("notes").clear(); tx.oncomplete = resolve; tx.onerror = () => reject(tx.error); tx.onabort = () => reject(tx.error); });
        if ((await all("notes")).length || await digest(await all("keys")) !== keySHA256) throw new Error("cache eviction changed retained keys");
        return { evictedNoteRows: notes.length, retainedKeyRows: keys.length, keySHA256, pendingRows: 0, uploadRows: 0 };
      } finally { db.close(); }
    });
    assert.ok(proof.evictedNoteRows > 0);
    assert.ok(proof.retainedKeyRows > 0);
    log("owned-content-cache-evicted-retained-keys-unchanged");
    return proof;
  }
  if (config.phase === "seed") {
    log("native-device-key-enrollment");
    await page.getByRole("button", { name: "Set up encryption key", exact: true }).click();
    const code = page.locator('[aria-label="Recovery code"]');
    await confirmed(code.waitFor());
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download as a text file", exact: true }).click();
    const download = await downloadPromise;
    const recoveryPath = path.join(path.dirname(config.expected), "recovery-code.txt");
    await download.saveAs(recoveryPath); await chmod(recoveryPath, 0o600);
    const recoveryText = await readFile(recoveryPath, "utf8");
    const match = recoveryText.match(/\b([A-Z0-9]{4}(?:-[A-Z0-9]{4}){6})\b/);
    assert.ok(match);
    await page.getByRole("button", { name: "I saved it", exact: true }).click();
    const group = page.getByLabel(/Type group \d of 7 from your saved copy/);
    const label = await group.evaluate(input => input.labels[0].textContent);
    const number = Number(label.match(/Type group (\d) of 7/)[1]);
    await replaceText(page, group, match[1].split("-")[number - 1]);
    log("native-recovery-copy-confirmation");
    await page.getByRole("button", { name: "Save recovery code", exact: true }).click();
    await confirmed(page.getByText("Recovery code saved. Keep it somewhere safe.", { exact: true }).waitFor());
    await page.getByRole("navigation", { name: "Primary", exact: true }).getByRole("button", { name: "Notes", exact: true }).click();
    log("old-client-encrypted-notebook-create");
    const containerResponse = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/api/v1/containers" && response.ok());
    await page.getByRole("button", { name: "＋ New notebook", exact: true }).click();
    const container = await (await confirmed(containerResponse)).json();
    assert.equal(typeof container.id, "string");
    await confirmed(page.getByText(notebook, { exact: true }).first().waitFor());
    await page.locator('.note-list[aria-busy="false"]').waitFor();
    log("old-client-encrypted-page-create");
    const created = saved();
    const newPage = page.getByRole("button", { name: "New page", exact: true });
    console.log(JSON.stringify({ at: new Date().toISOString(), stage, phase: config.phase, newPageControls: await newPage.count() }));
    log("old-client-new-page-click");
    await newPage.click();
    log("old-client-new-page-save-response");
    const objectID = new URL((await confirmed(created)).url()).pathname.split("/").pop();
    assert.match(objectID, /^obj_/);
    log("old-client-page-title-input");
    const titled = saved();
    const titleControl = page.getByLabel("Page title", { exact: true });
    await replaceContentText(page, titleControl, title);
    const actualTitle = await titleControl.inputValue();
    console.log(JSON.stringify({ at: new Date().toISOString(), stage: "title-input-proof", phase: config.phase, matches: actualTitle === title, actualLength: actualTitle.length, expectedLength: title.length, actualSHA256: sha256(actualTitle), expectedSHA256: sha256(title), prefixMatches: actualTitle.startsWith(title.slice(0, -1)), unicodeSuffixMatches: actualTitle.endsWith(title.slice(-1)) }));
    assert.equal(actualTitle, title);
    await confirmed(titled);
    log("old-client-page-body-input");
    const written = saved();
    await page.getByRole("button", { name: "Add text", exact: true }).click();
    await page.locator(".bn-editor").first().click();
    await appendText(page, body); await confirmed(written);
    assert.equal(await page.locator(".bn-editor").first().innerText(), body);
    assert.ok(confirmations >= 2);
    await flushPage();
    const expected = { containerID: container.id, objectID, ...await readState(objectID) };
    expected.cacheEviction = await dropOwnedContentCache();
    await writeFile(config.expected, JSON.stringify(expected) + "\n", { mode: 0o600, flag: "wx" });
    log("old-client-encrypted-content-seeded");
  } else {
    assert.equal(config.phase, "verify");
    const expected = JSON.parse(await readFile(config.expected, "utf8"));
    log("candidate-ciphertext-and-identity-retained");
    assert.deepEqual(await readState(expected.objectID), { identity: expected.identity, objectSHA256: expected.objectSHA256 });
    await page.goto(config.notes + "/#/" + expected.containerID);
    await confirmed(page.getByText(notebook, { exact: true }).first().waitFor());
    await page.locator(".note-list").getByText(title, { exact: true }).click();
    await page.getByLabel("Page title", { exact: true }).waitFor();
    assert.equal(await page.getByLabel("Page title", { exact: true }).inputValue(), title);
    await page.waitForFunction(text => document.querySelector(".bn-editor")?.innerText === text, body);
    log("candidate-old-client-key-decryption-passed");
    await captureEditorProof("before-edit");
    log("candidate-append-edit-input");
    const written = saved();
    const editor = page.locator(".bn-editor").first();
    const caret = await focusParagraphEnd(page, editor.locator("p").first());
    console.log(JSON.stringify({ at: new Date().toISOString(), stage: "original-paragraph-caret-proof", phase: config.phase, ...caret }));
    assert.deepEqual(caret, { inside: true, collapsed: true, atEnd: true });
    await appendText(page, " " + edited); await confirmed(written);
    await captureEditorProof("after-input");
    await page.waitForFunction(text => document.querySelector(".bn-editor")?.innerText === text, body + " " + edited);
    log("candidate-edit-visible-before-save");
    await flushPage();
    await captureEditorProof("after-save");
    const changed = await readState(expected.objectID);
    assert.deepEqual(changed.identity, expected.identity);
    assert.notEqual(changed.objectSHA256, expected.objectSHA256);
    await dropOwnedContentCache();
    log("candidate-cache-evicted-reload");
    await page.goto(config.notes + "/#/" + expected.containerID);
    await page.locator(".note-list").getByText(title, { exact: true }).click();
    await captureEditorProof("after-reload-open");
    await page.waitForFunction(text => document.querySelector(".bn-editor")?.innerText === text, body + " " + edited);
    log("candidate-edit-save-reload-decryption-passed");
  }
  assert.equal(badWire, false, "plaintext entered an encrypted content request");
  assert.equal(unexpectedDialog, false, "unexpected user consent dialog");
  log("passed");
  exitCode = 0;
} catch (error) {
  try { await captureEditorProof?.("failure"); } catch { /* Primary safe diagnostic still follows. */ }
  // Locator call logs can contain entered credentials/recovery codes. Keep only stage and class.
  const pages = [];
  for (const page of context?.pages() ?? []) {
    try {
      const url = new URL(page.url());
      pages.push({ origin: url.origin, route: url.pathname.split("/").slice(0, 4).join("/"),
        ui: await page.evaluate(() => ({ username: Boolean(document.querySelector('input[autocomplete="username"]')), password: Boolean(document.querySelector('input[type="password"]')), ssoLink: Boolean(document.querySelector('a[href="/api/v1/auth/oidc/login"]')), notebookNavigation: Boolean(document.querySelector("#notebook-navigation")), hasRootChildren: Boolean(document.querySelector("#root")?.childElementCount), secureContext: window.isSecureContext, webCrypto: Boolean(window.crypto?.subtle), formEvents: window.__kyqAuthEvents ?? null, alertPresent: Boolean(document.querySelector('[role="alert"]')), alertCategories: (() => { const text = document.querySelector('[role="alert"]')?.textContent ?? ""; return { csrf: /csrf|cross.site/i.test(text), credentials: /invalid.*password|invalid.*credential|incorrect.*password/i.test(text), responseShape: /invalid.*response|invalid.*reply|reported.*no account/i.test(text), expired: /expired|cancelled/i.test(text) }; })() })) });
    } catch { pages.push({ diagnosticUnavailable: true }); }
  }
  console.error(JSON.stringify({ at: new Date().toISOString(), stage, phase: config.phase, failed: true, errorClass: error.name, failureKinds: { strictLocator: /strict mode violation/i.test(error.message), timeout: /timeout/i.test(error.message), closed: /has been closed/i.test(error.message), unsupportedKey: /unknown key|unsupported key/i.test(error.message), selector: /selector|aria-role/i.test(error.message), invalidURL: /invalid url/i.test(error.message), assertion: error.name === "AssertionError" }, sourceLocations: Array.from((error.stack ?? "").matchAll(/notes_browser\.mjs:(\d+):(\d+)/g), match => ({ line: Number(match[1]), column: Number(match[2]) })).slice(0, 5), operation: error.message.match(/^(?:page|locator|browserContext|browser)\.[A-Za-z]+/)?.[0], pages, diagnostics }));
} finally {
  if (context) {
    const closed = await Promise.race([context.close().then(() => true), new Promise(resolve => setTimeout(() => resolve(false), 5000))]);
    if (!closed) exitCode = 1;
  }
  for (const socket of sockets) socket.destroy();
  proxy.close();
}
process.exit(exitCode);
