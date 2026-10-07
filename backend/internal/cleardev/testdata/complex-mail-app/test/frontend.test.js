import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { pathToFileURL } from "node:url";

test("browser page shows import form and count placeholders", () => {
  const html = fs.readFileSync("frontend/index.html", "utf8");
  assert.match(html, /id="import-form"/);
  assert.match(html, /data-testid="accepted-count"/);
  assert.match(html, /data-testid="rejected-count"/);
  assert.match(html, /data-testid="duplicate-count"/);
  const script = fs.readFileSync("frontend/app.js", "utf8");
  assert.match(script, /\/api\/import/);
});

test("form submit writes API counts onto the page", async (t) => {
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
  const previousFetch = Object.getOwnPropertyDescriptor(globalThis, "fetch");
  t.after(() => {
    if (previousDocument) {
      Object.defineProperty(globalThis, "document", previousDocument);
    } else {
      delete globalThis.document;
    }
    if (previousFetch) {
      Object.defineProperty(globalThis, "fetch", previousFetch);
    } else {
      delete globalThis.fetch;
    }
  });
  const accepted = { textContent: "0" };
  const rejected = { textContent: "0" };
  const duplicates = { textContent: "0" };
  const textarea = {
    value: "user@example.com\n user@example.com \nUSER@example.com\nnot-an-email\nfoo@example.com",
  };
  const listeners = {};
  const form = {
    addEventListener(type, fn) {
      listeners[type] = fn;
    },
  };
  globalThis.document = {
    querySelector(sel) {
      if (sel === "#import-form") return form;
      if (sel === "#emails") return textarea;
      if (sel === '[data-testid="accepted-count"]') return accepted;
      if (sel === '[data-testid="rejected-count"]') return rejected;
      if (sel === '[data-testid="duplicate-count"]') return duplicates;
      return null;
    },
  };
  let posted;
  globalThis.fetch = async (url, opts) => {
    posted = { url, body: JSON.parse(opts.body) };
    return {
      ok: true,
      json: async () => ({ accepted: 2, rejected: 1, duplicates: 2 }),
    };
  };
  await import(`${pathToFileURL("frontend/app.js").href}?form=${Date.now()}`);
  await listeners.submit({ preventDefault() {} });
  assert.equal(posted.url, "/api/import");
  assert.deepEqual(posted.body.emails, textarea.value.split(/\r?\n/));
  assert.equal(accepted.textContent, "2");
  assert.equal(rejected.textContent, "1");
  assert.equal(duplicates.textContent, "2");
});
