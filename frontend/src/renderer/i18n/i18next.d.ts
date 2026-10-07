import "i18next";
import type { enMessages } from "./messages";

declare module "i18next" {
	interface CustomTypeOptions {
		defaultNS: "translation";
		returnNull: false;
		keySeparator: false;
		resources: {
			translation: typeof enMessages;
		};
	}
}
