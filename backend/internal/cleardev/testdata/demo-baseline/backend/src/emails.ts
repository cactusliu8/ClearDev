export type EmailRecord = {
  raw: string;
  normalized: string;
};

export function normalizeEmail(input: string): string {
  return input.trim().toLowerCase();
}

// This local contact importer accepts ordinary addresses, not the full RFC
// mailbox syntax (display names, comments, or quoted local parts).
export function isValidEmail(input: string): boolean {
  const normalized = normalizeEmail(input);
  if (normalized.length > 254) return false;
  const parts = normalized.split("@");
  if (parts.length !== 2) return false;
  const [local, domain] = parts;
  if (!local || local.length > 64 || local.startsWith(".") || local.endsWith(".") || local.includes("..")) return false;
  if (!/^[a-z0-9.!#$%&'*+/=?^_`{|}~-]+$/.test(local)) return false;
  const labels = domain.split(".");
  return labels.length >= 2 && labels.every((label) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label));
}

export function classifyEmails(inputs: string[]): {
  accepted: string[];
  rejected: number;
  duplicates: number;
} {
  const seen = new Set<string>();
  const accepted: string[] = [];
  let rejected = 0;
  let duplicates = 0;
  for (const input of inputs) {
    const normalized = normalizeEmail(input);
    if (!isValidEmail(normalized)) {
      rejected++;
    } else if (seen.has(normalized)) {
      duplicates++;
    } else {
      seen.add(normalized);
      accepted.push(normalized);
    }
  }
  return { accepted, rejected, duplicates };
}
