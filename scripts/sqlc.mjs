#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const sqlcModule = "github.com/sqlc-dev/sqlc";
const sqlcVersion = "v1.31.1";
const sqlcSum = "h1:+V+BjBJfFNPX/RFfL8eiZD9jk9lVJUEGGllWvnYNqbc=";
const sqlcGoModSum = "h1:6ZPww/Jd3G6MzJeW6NrqizjL+52vYNaaXP9yMeJ/Nao=";
const fixedGoVersion = "go1.25.7";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, "..");
const backendDir = path.join(repoRoot, "backend");

export function localOnlyGoProxy(value) {
	const entries = value.split(",").map((entry) => entry.trim()).filter(Boolean);
	return entries.length > 0 && entries.every((entry) => entry === "off" || entry.startsWith("file://"));
}

export function validateSqlcDownload(download) {
	if (download.Path !== sqlcModule || download.Version !== sqlcVersion || download.Sum !== sqlcSum || download.GoModSum !== sqlcGoModSum) {
		throw new Error("本地 sqlc 缓存与仓库固定版本或校验值不一致");
	}
	if (!path.isAbsolute(download.Zip || "") || !path.isAbsolute(download.GoMod || "")) {
		throw new Error("本地 sqlc 缓存路径无效");
	}
}

export function makeSqlcGo125Compatible(source) {
	const original = `module ${sqlcModule}\n\ngo 1.26.0\n\ntoolchain go1.26.2\n`;
	const compatible = `module ${sqlcModule}\n\ngo 1.25.0\n`;
	if (!source.startsWith(original)) {
		throw new Error("sqlc 模块声明与已核实的 v1.31.1 内容不一致");
	}
	return compatible + source.slice(original.length);
}

function capture(command, args, options = {}) {
	const result = spawnSync(command, args, { ...options, encoding: "utf8" });
	if (result.status !== 0) {
		throw new Error((result.stderr || result.stdout || `${command} 运行失败`).trim());
	}
	return result.stdout.trim();
}

function run(command, args, options = {}) {
	const result = spawnSync(command, args, { ...options, stdio: "inherit" });
	if (result.status !== 0) {
		throw new Error(`${command} 运行失败，退出码 ${result.status ?? "未知"}`);
	}
}

function downloadMetadata() {
	const result = spawnSync("go", ["mod", "download", "-json", `${sqlcModule}@${sqlcVersion}`], {
		cwd: backendDir,
		encoding: "utf8",
	});
	let download;
	try {
		download = JSON.parse(result.stdout);
	} catch {
		throw new Error((result.stderr || result.stdout || "无法读取本地 sqlc 缓存").trim());
	}
	validateSqlcDownload(download);
	return download;
}

export function main() {
	if (capture("go", ["env", "GOVERSION"], { cwd: backendDir }) !== fixedGoVersion) {
		throw new Error(`sqlc 生成必须使用固定 ${fixedGoVersion}`);
	}
	if (capture("go", ["env", "GOTOOLCHAIN"], { cwd: backendDir }) !== "local") {
		throw new Error("sqlc 生成必须设置 GOTOOLCHAIN=local");
	}
	if (!localOnlyGoProxy(capture("go", ["env", "GOPROXY"], { cwd: backendDir }))) {
		throw new Error("sqlc 生成只允许使用本地 Go 模块缓存");
	}

	const download = downloadMetadata();
	const workDir = mkdtempSync(path.join(tmpdir(), "cleardev-sqlc-"));
	try {
		run("unzip", ["-q", download.Zip, "-d", workDir], { cwd: repoRoot });
		const sourceDir = path.join(workDir, `${sqlcModule}@${sqlcVersion}`);
		const goModPath = path.join(sourceDir, "go.mod");
		const cachedGoMod = readFileSync(download.GoMod, "utf8");
		const sourceGoMod = readFileSync(goModPath, "utf8");
		if (sourceGoMod !== cachedGoMod) {
			throw new Error("sqlc 源码包与本地模块声明不一致");
		}
		chmodSync(goModPath, 0o600);
		writeFileSync(goModPath, makeSqlcGo125Compatible(sourceGoMod));

		const binary = path.join(workDir, "sqlc");
		run("go", ["build", "-mod=readonly", "-o", binary, "./cmd/sqlc"], { cwd: sourceDir });
		if (capture(binary, ["version"], { cwd: backendDir }) !== sqlcVersion) {
			throw new Error(`生成器版本不是 ${sqlcVersion}`);
		}
		run(binary, ["generate"], { cwd: backendDir });
	} finally {
		rmSync(workDir, { recursive: true, force: true });
	}
}

const isDirect = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isDirect) {
	try {
		main();
	} catch (err) {
		console.error(err.message);
		process.exit(1);
	}
}
