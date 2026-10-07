import { createHash } from "node:crypto";
import { access, chmod, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

import { requirePackagedRegularFiles } from "./cleardev-demo-files.mjs";

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const frontendRoot = path.join(repositoryRoot, "frontend");
const sourceScope = [
	"backend",
	"frontend",
	"package.json",
	"package-lock.json",
	"scripts/cleardev-demo.mjs",
	"scripts/cleardev-demo-files.mjs",
];

await main();

async function main() {
	if (process.platform !== "linux") {
		throw new Error("cleardev:demo currently requires Linux with a real desktop, Xvfb, x11vnc, and TigerVNC Viewer");
	}
	await visibleDesktopPreflight();
	const candidateCommit = (await capture("git", ["rev-parse", "HEAD"], { cwd: repositoryRoot })).trim();
	if (!/^[0-9a-f]{40}$/.test(candidateCommit)) throw new Error(`Git returned an invalid candidate commit: ${candidateCommit}`);
	await requireCleanSource();

	const buildSourcePath = path.join(frontendRoot, "build", "cleardev-build-source.json");
	let buildSourceCreated = false;
	try {
		await mkdir(path.dirname(buildSourcePath), { recursive: true });
		await writeFile(
			buildSourcePath,
			`${JSON.stringify({
				schemaVersion: 1,
				candidateCommit,
				sourceClean: true,
				sourceScope,
				builtAt: new Date().toISOString(),
			}, null, 2)}\n`,
			{ encoding: "utf8", mode: 0o644, flag: "wx" },
		);
		buildSourceCreated = true;
		await chmod(buildSourcePath, 0o644);

		console.log(`Building one Electron package from clean candidate ${candidateCommit}`);
		await run("npm", ["--prefix", "frontend", "run", "package"], {
			cwd: repositoryRoot,
			env: { ...process.env, AO_CLEARDEV_BUILD_SOURCE_FILE: buildSourcePath },
		});
		await requireCleanSource();

		const packaged = await locateLinuxPackage();
		await requirePackagedRegularFiles(path.dirname(packaged.electron), {
			electron: packaged.electron,
			daemon: packaged.daemon,
			agentBrowser: packaged.agentBrowser,
		});
		const buildSource = JSON.parse(await readFile(packaged.buildSource, "utf8"));
		if (buildSource.candidateCommit !== candidateCommit || buildSource.sourceClean !== true) {
			throw new Error("Packaged build source does not match the clean candidate used by this command");
		}
		for (const executable of [packaged.electron, packaged.daemon, packaged.agentBrowser]) {
			const mode = (await stat(executable)).mode & 0o777;
			if (mode !== 0o755) throw new Error(`Packaged executable ${executable} has mode ${mode.toString(8)}, want 755`);
		}
		const packageFingerprint = await packageSHA256(packaged);

		for (let runNumber = 1; runNumber <= 2; runNumber += 1) {
			console.log(`Starting visible isolated demonstration ${runNumber}/2 from the same package`);
			await run(packaged.daemon, ["cleardev", "demo"], {
				cwd: repositoryRoot,
				env: {
					...process.env,
					AO_ELECTRON_BIN: packaged.electron,
					AO_CLEARDEV_EXPECTED_CANDIDATE: candidateCommit,
				},
			});
			const afterRun = await packageSHA256(packaged);
			if (afterRun !== packageFingerprint) throw new Error(`Packaged runtime changed during demonstration ${runNumber}`);
			await requireCleanSource();
		}
		console.log(`Both visible demonstrations completed from candidate ${candidateCommit}`);
	} finally {
		if (buildSourceCreated) await rm(buildSourcePath, { force: true });
	}
}

async function visibleDesktopPreflight() {
	if (!process.env.DISPLAY?.trim()) throw new Error("A real desktop DISPLAY is required; refusing an invisible demonstration");
	for (const command of ["git", "npm", "Xvfb", "x11vnc", "vncviewer", "xdotool", "xwd"]) {
		await findExecutable(command);
	}
	await capture("xdotool", ["getmouselocation", "--shell"], { cwd: repositoryRoot });
}

async function requireCleanSource() {
	const status = await capture("git", ["status", "--porcelain=v1", "--untracked-files=all", "--", ...sourceScope], {
		cwd: repositoryRoot,
	});
	if (status.trim()) throw new Error(`Source inputs are not clean:\n${status.trim()}`);
}

async function locateLinuxPackage() {
	const outRoot = path.join(frontendRoot, "out");
	const entries = await readdir(outRoot, { withFileTypes: true });
	const candidates = entries
		.filter((entry) => entry.isDirectory() && entry.name.endsWith("-linux-x64"))
		.map((entry) => path.join(outRoot, entry.name));
	if (candidates.length !== 1) throw new Error(`Expected one Linux x64 package, found ${candidates.length}`);
	const packageRoot = candidates[0];
	const packaged = {
		electron: path.join(packageRoot, "agent-orchestrator"),
		daemon: path.join(packageRoot, "resources", "daemon", "ao"),
		agentBrowser: path.join(packageRoot, "resources", "agent-browser", "agent-browser"),
		buildSource: path.join(packageRoot, "resources", "cleardev-build-source.json"),
	};
	await Promise.all(Object.values(packaged).map((file) => access(file)));
	return packaged;
}

async function packageSHA256(packaged) {
	const digest = createHash("sha256");
	for (const [name, file] of Object.entries(packaged).sort(([a], [b]) => a.localeCompare(b))) {
		digest.update(name);
		digest.update(await readFile(file));
	}
	return digest.digest("hex");
}

async function findExecutable(command) {
	for (const directory of (process.env.PATH ?? "").split(path.delimiter)) {
		if (!directory) continue;
		const candidate = path.join(directory, command);
		try {
			await access(candidate, 1);
			return candidate;
		} catch {
			// Continue through PATH.
		}
	}
	throw new Error(`${command} is required for the visible demonstration`);
}

async function capture(command, args, options) {
	let output = "";
	await run(command, args, {
		...options,
		stdio: ["ignore", "pipe", "pipe"],
		onOutput: (text) => {
			output += text;
		},
	});
	return output;
}

async function run(command, args, options = {}) {
	await new Promise((resolve, reject) => {
		const child = spawn(command, args, {
			cwd: options.cwd,
			env: options.env ?? process.env,
			stdio: options.stdio ?? "inherit",
		});
		if (options.onOutput) {
			child.stdout?.on("data", (chunk) => options.onOutput(chunk.toString("utf8")));
			child.stderr?.on("data", (chunk) => options.onOutput(chunk.toString("utf8")));
		}
		child.once("error", reject);
		child.once("exit", (code, signal) => {
			if (code === 0) resolve();
			else reject(new Error(`${command} exited with ${signal ?? `code ${code ?? "unknown"}`}`));
		});
	});
}
