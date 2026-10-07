import assert from "node:assert/strict";
import test from "node:test";

import { localOnlyGoProxy, makeSqlcGo125Compatible, validateSqlcDownload } from "./sqlc.mjs";

test("sqlc compatibility changes only the checked toolchain declaration", () => {
	const source = "module github.com/sqlc-dev/sqlc\n\ngo 1.26.0\n\ntoolchain go1.26.2\n\nrequire example.com/dependency v1.0.0\n";
	assert.equal(
		makeSqlcGo125Compatible(source),
		"module github.com/sqlc-dev/sqlc\n\ngo 1.25.0\n\nrequire example.com/dependency v1.0.0\n",
	);
	assert.throws(() => makeSqlcGo125Compatible(source.replace("go 1.26.0", "go 1.26.1")), /模块声明/);
});

test("sqlc download must match the fixed module checksums", () => {
	const download = {
		Path: "github.com/sqlc-dev/sqlc",
		Version: "v1.31.1",
		Sum: "h1:+V+BjBJfFNPX/RFfL8eiZD9jk9lVJUEGGllWvnYNqbc=",
		GoModSum: "h1:6ZPww/Jd3G6MzJeW6NrqizjL+52vYNaaXP9yMeJ/Nao=",
		Zip: "/cache/sqlc.zip",
		GoMod: "/cache/sqlc.mod",
	};
	assert.doesNotThrow(() => validateSqlcDownload(download));
	assert.throws(() => validateSqlcDownload({ ...download, Sum: "h1:changed" }), /校验值/);
});

test("sqlc generation rejects network module proxies", () => {
	assert.equal(localOnlyGoProxy("file:///cache/download"), true);
	assert.equal(localOnlyGoProxy("file:///cache/download,off"), true);
	assert.equal(localOnlyGoProxy("off"), true);
	assert.equal(localOnlyGoProxy("https://proxy.golang.org,direct"), false);
});
