import { createHash } from "node:crypto";
import { chmod, readFile } from "node:fs/promises";

export async function fileSHA256(file) {
	try {
		const contents = await readFile(file);
		return createHash("sha256").update(contents).digest("hex");
	} catch (error) {
		if (error?.code === "ENOENT") return "";
		throw error;
	}
}

export async function ensureVerifiedExecutable(file, expectedSHA256, platform = process.platform) {
	const actual = await fileSHA256(file);
	if (actual !== expectedSHA256) {
		throw new Error(`agent-browser checksum mismatch: expected ${expectedSHA256}, received ${actual || "missing file"}`);
	}
	if (platform !== "win32") await chmod(file, 0o755);
	return actual;
}
