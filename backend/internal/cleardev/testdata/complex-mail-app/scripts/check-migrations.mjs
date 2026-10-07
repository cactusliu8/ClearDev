import fs from "node:fs";
import path from "node:path";

const root = "migrations";
if (!fs.existsSync(root)) {
  console.error("check:migrations: missing migrations/");
  process.exit(1);
}

const blocked = /\b(drop\s+table|drop\s+database|attach\s+database|load_extension|pragma\s+writable_schema)\b/i;
let files = 0;

function walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const next = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(next);
      continue;
    }
    if (!entry.name.endsWith(".sql")) {
      continue;
    }
    files += 1;
    const text = fs.readFileSync(next, "utf8");
    if (blocked.test(text)) {
      console.error(`check:migrations: blocked statement in ${next}`);
      process.exit(1);
    }
  }
}

walk(root);
if (files === 0) {
  console.error("check:migrations: no .sql files");
  process.exit(1);
}
console.log(`check:migrations: accepted ${files} file(s)`);
