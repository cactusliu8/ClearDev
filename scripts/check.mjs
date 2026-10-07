#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(scriptDir, "..");

export function loadPin(root = repoRoot) {
	return JSON.parse(readFileSync(path.join(root, "docs/cleardev/development/toolchain.json"), "utf8"));
}

export function listSteps(kind, options = {}) {
	const root = options.root ?? repoRoot;
	const backend = path.join(root, "backend");
	const startSha = options.startSha ?? null;
	const pin = options.pin ?? loadPin(root);
	const packages = options.packages ?? [];
	const handoff = ["run", "./cmd/cleardev-handoff"];
	const frontendDir = path.join(root, "frontend");

	const quick = [
		{
			name: "toolchain",
			cwd: backend,
			command: "go",
			args: [...handoff, "-mode=toolchain", "-require-go", "-require-node", "-root", root],
		},
		{
			name: "check-script-tests",
			cwd: root,
			command: "node",
			args: ["--test", "scripts/check.test.mjs", "scripts/sqlc.test.mjs"],
		},
	];
	const backendSteps = () => [
		{
			name: "toolchain-go",
			cwd: backend,
			command: "go",
			args: [...handoff, "-mode=toolchain", "-require-go", "-root", root],
		},
		{
			name: "gofmt",
			cwd: backend,
			command: "node",
			args: [path.join(scriptDir, "check.mjs"), "_gofmt", backend],
		},
		{
			name: "build",
			cwd: backend,
			command: "go",
			args: ["build", "-p", "2", "./..."],
		},
		{
			name: "test",
			cwd: backend,
			command: "go",
			args: ["test", "-p", "2", "-timeout", "30m", "./..."],
		},
		{
			name: "race",
			cwd: backend,
			command: "go",
			args: ["test", "-race", "-p", "2", "-timeout", "50m", "./..."],
		},
		{
			name: "vet",
			cwd: backend,
			command: "go",
			args: ["vet", "-p", "2", "./..."],
		},
		{
			name: "lint",
			cwd: backend,
			command: "go",
			args: lintArgs(pin, startSha),
		},
	];
	const frontend = [
		{
			name: "toolchain-node",
			cwd: backend,
			command: "go",
			args: [...handoff, "-mode=toolchain", "-require-node", "-root", root],
		},
		{
			name: "shared",
			cwd: root,
			command: "npm",
			args: ["run", "shared:check"],
		},
		{
			name: "browser-runtime",
			cwd: frontendDir,
			command: "npm",
			args: ["run", "browser-runtime:prepare", "--", "--quiet", "--offline"],
		},
		{
			name: "typecheck",
			cwd: frontendDir,
			command: "npm",
			args: ["run", "typecheck"],
		},
		{
			name: "typecheck-e2e",
			cwd: frontendDir,
			command: "npm",
			args: ["run", "typecheck:e2e"],
		},
		{
			name: "test",
			cwd: frontendDir,
			command: "npm",
			args: ["run", "test"],
		},
		{
			name: "build",
			cwd: frontendDir,
			command: "npm",
			args: ["run", "build"],
		},
	];
	const api = [
		{
			name: "toolchain-api",
			cwd: backend,
			command: "go",
			args: [...handoff, "-mode=toolchain", "-require-go", "-require-node", "-root", root],
		},
		{
			name: "api",
			cwd: root,
			command: "npm",
			args: ["run", "api"],
		},
		{
			name: "api-drift",
			cwd: root,
			command: "git",
			args: ["diff", "--exit-code", "--", "frontend/src/api/schema.ts"],
		},
	];
	const lint = () => [
		{
			name: "toolchain-go",
			cwd: backend,
			command: "go",
			args: [...handoff, "-mode=toolchain", "-require-go", "-root", root],
		},
		{
			name: "lint",
			cwd: backend,
			command: "go",
			args: lintArgs(pin, startSha),
		},
	];
	const scopedBackend = (operation) => {
		if (packages.length === 0 || packages.some((item) => typeof item !== "string" || !item.startsWith("./"))) {
			throw new Error("后台范围检查需要显式提供一个或多个以 ./ 开头的 Go 包范围");
		}
		const toolchain = {
			name: "toolchain-go",
			cwd: backend,
			command: "go",
			args: [...handoff, "-mode=toolchain", "-require-go", "-root", root],
		};
		switch (operation) {
			case "test":
				return [toolchain, {
					name: "test",
					cwd: backend,
					command: "go",
					args: ["test", "-p", "2", "-timeout", "30m", ...packages],
				}];
			case "race":
				return [toolchain, {
					name: "race",
					cwd: backend,
					command: "go",
					args: ["test", "-race", "-p", "2", "-timeout", "50m", ...packages],
				}];
			case "lint":
				return [toolchain, {
					name: "lint",
					cwd: backend,
					command: "go",
					args: lintArgs(pin, startSha, packages),
				}];
			default:
				throw new Error(`未知后台范围检查 ${operation}`);
		}
	};

	switch (kind) {
		case "quick":
			return quick;
		case "backend":
			return backendSteps();
		case "backend-test":
			return scopedBackend("test");
		case "backend-race":
			return scopedBackend("race");
		case "backend-lint":
			return scopedBackend("lint");
		case "frontend":
			return frontend;
		case "api":
			return api;
		case "lint":
			return lint();
		case "all":
			return [...quick, ...backendSteps(), ...frontend, ...api];
		default:
			throw new Error(`未知检查入口 ${kind}，应为 quick、backend、backend-test、backend-race、backend-lint、frontend、api、all 或 lint`);
	}
}

export function lintArgs(pin, startSha, packages = []) {
	const version = pin?.golangciLint ?? "2.12.2";
	const args = [
		"run",
		"-p=2",
		`github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${version}`,
		"run",
		"--path-mode=abs",
		"--concurrency=2",
	];
	if (!startSha) {
		throw new Error("增量 lint 需要当前阶段计划起始提交，缺少 --new-from-rev 基线");
	}
	args.push(`--new-from-rev=${startSha}`);
	args.push(...packages);
	return args;
}

export function stepNames(kind, options) {
	return listSteps(kind, options).map((step) => step.name);
}

function gitSafeEnv(root) {
	const env = { ...process.env };
	const n = Number(env.GIT_CONFIG_COUNT || "0");
	env.GIT_CONFIG_COUNT = String(n + 1);
	env[`GIT_CONFIG_KEY_${n}`] = "safe.directory";
	env[`GIT_CONFIG_VALUE_${n}`] = root;
	return env;
}

export function localModuleProxy(moduleCache) {
	if (!path.isAbsolute(moduleCache)) {
		throw new Error(`Go 模块缓存不是绝对路径: ${moduleCache}`);
	}
	return pathToFileURL(path.join(moduleCache, "cache", "download")).href;
}

function localCheckEnv(root) {
	const env = gitSafeEnv(root);
	env.GOTOOLCHAIN = "local";
	const result = spawnSync("go", ["env", "GOMODCACHE"], {
		cwd: root,
		encoding: "utf8",
		env,
	});
	if (result.status !== 0) {
		throw new Error((result.stderr || result.stdout || "无法读取本地 Go 模块缓存").trim());
	}
	const moduleCache = result.stdout.trim();
	if (!moduleCache) {
		throw new Error("本地 Go 模块缓存路径为空");
	}
	env.GOPROXY = localModuleProxy(moduleCache);
	env.GONOPROXY = "none";
	env.GOSUMDB = "off";
	env.CLEARDEV_OFFLINE_CHECK = "1";
	return env;
}

function checkGofmt(backendDir) {
	const result = spawnSync("gofmt", ["-l", "."], { cwd: backendDir, encoding: "utf8" });
	if (result.status !== 0) {
		console.error(result.stderr || result.stdout || "gofmt failed");
		process.exit(result.status === null ? 1 : result.status);
	}
	const files = result.stdout.trim();
	if (files) {
		console.error("These files need gofmt:");
		console.error(files);
		process.exit(1);
	}
}

function runStep(step, env) {
	console.error(`+ ${step.name}`);
	const result = spawnSync(step.command, step.args, {
		cwd: step.cwd,
		stdio: "inherit",
		env,
	});
	if (result.status !== 0) {
		process.exit(result.status === null ? 1 : result.status);
	}
}

export function main(argv = process.argv.slice(2), options = {}) {
	const kind = argv[0];
	if (kind === "_gofmt") {
		checkGofmt(argv[1]);
		return;
	}
	if (!kind) {
		console.error("usage: node scripts/check.mjs <quick|backend|backend-test|backend-race|backend-lint|frontend|api|all|lint> [./package ...]");
		process.exit(2);
	}
	const root = options.root ?? repoRoot;
	let env;
	try {
		env = localCheckEnv(root);
	} catch (err) {
		console.error(err.message);
		process.exit(2);
	}
	let startSha = options.startSha;
	if (["backend", "backend-lint", "lint", "all"].includes(kind) && !startSha) {
		startSha = (process.env.CLEARDEV_LINT_BASE ?? "").trim();
		if (!/^[0-9a-f]{7,40}$/i.test(startSha)) {
			console.error("增量 lint 需要基线 Git 提交：设置 CLEARDEV_LINT_BASE=<提交号>，例如你功能分支的起点提交");
			process.exit(2);
		}
	}
	let steps;
	try {
		steps = listSteps(kind, { root, startSha, pin: loadPin(root), packages: argv.slice(1) });
	} catch (err) {
		console.error(err.message);
		process.exit(2);
	}
	for (const step of steps) {
		runStep(step, env);
	}
}

const isDirect = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isDirect) {
	main();
}
