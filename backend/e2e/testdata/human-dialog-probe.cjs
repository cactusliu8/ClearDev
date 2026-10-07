const { app, dialog } = require("electron");

const title = process.argv[2] || "Approve direction change";
const detail = process.argv[3] || "probe";

app.whenReady().then(async () => {
	const result = await dialog.showMessageBox({
		type: "question",
		buttons: ["Approve", "Reject", "Later"],
		defaultId: 2,
		cancelId: 2,
		noLink: true,
		title,
		message: title,
		detail,
	});
	process.stdout.write(`RESPONSE ${result.response}\n`);
	app.quit();
});
