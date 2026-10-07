import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";

const roots = ["backend", "frontend"];
const files = [];

function walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const next = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(next);
      continue;
    }
    if (entry.name.endsWith(".ts")) {
      files.push(next);
    }
  }
}

for (const root of roots) {
  if (fs.existsSync(root)) {
    walk(root);
  }
}

if (files.length === 0) {
  console.error("build: no TypeScript sources found");
  process.exit(1);
}

for (const file of files) {
  const result = spawnSync(
    process.execPath,
    ["--experimental-strip-types", "--experimental-sqlite", "--check", file],
    { stdio: "inherit" },
  );
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

console.log(`build: checked ${files.length} TypeScript file(s)`);
