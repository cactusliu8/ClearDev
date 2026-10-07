export type EmailRecord = {
  raw: string;
  normalized: string;
};

export function normalizeEmail(input: string): string {
  throw new Error("not implemented");
}

export function isValidEmail(input: string): boolean {
  throw new Error("not implemented");
}

export function classifyEmails(inputs: string[]): {
  accepted: string[];
  rejected: number;
  duplicates: number;
} {
  throw new Error("not implemented");
}
