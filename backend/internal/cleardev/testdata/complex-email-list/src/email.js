export function normalizeEmail(value) {
  return String(value ?? "").trim().toLowerCase();
}
