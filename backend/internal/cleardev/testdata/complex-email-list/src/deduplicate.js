import { normalizeEmail } from "./email.js";

export function deduplicateEmails(values) {
  const seen = new Set();
  const accepted = [];
  for (const value of values) {
    const normalized = normalizeEmail(value);
    if (!normalized || !normalized.includes("@") || seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    accepted.push(normalized);
  }
  return accepted;
}
