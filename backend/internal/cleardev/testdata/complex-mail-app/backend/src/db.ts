import { DatabaseSync } from "node:sqlite";
import fs from "node:fs";
import path from "node:path";

export function openDatabase(filePath: string): DatabaseSync {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  return new DatabaseSync(filePath);
}

export function applyMigrations(db: DatabaseSync, migrationsDir: string): void {
  const files = fs
    .readdirSync(migrationsDir)
    .filter((name) => name.endsWith(".sql"))
    .sort();
  for (const name of files) {
    const sql = fs.readFileSync(path.join(migrationsDir, name), "utf8");
    db.exec(sql);
  }
}
