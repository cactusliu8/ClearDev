import { lstat, realpath } from "node:fs/promises";
import path from "node:path";

export async function requirePackagedRegularFiles(packageRoot, files) {
	const realRoot = await realpath(packageRoot);
	for (const [name, file] of Object.entries(files)) {
		const info = await lstat(file);
		if (info.isSymbolicLink()) {
			throw new Error(`Packaged ${name} is a symbolic link: ${file}`);
		}
		if (!info.isFile()) {
			throw new Error(`Packaged ${name} is not a regular file: ${file}`);
		}
		const realFile = await realpath(file);
		const relative = path.relative(realRoot, realFile);
		if (relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
			throw new Error(`Packaged ${name} resolves outside package root: ${realFile}`);
		}
	}
}
