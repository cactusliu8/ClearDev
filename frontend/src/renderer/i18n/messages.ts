import en from "./en.json";
import zhCN from "./zh-CN.json";
import ja from "./ja.json";
import ko from "./ko.json";
import es from "./es.json";
import fr from "./fr.json";
import de from "./de.json";
import ptBR from "./pt-BR.json";
import type { AppLocale } from "./locales";
import blockerEN from "./blockers.en.json";
import blockerZH from "./blockers.zh-CN.json";

/** English is the source-of-truth catalog; keys are typed from it. */
export const enMessages = { ...en, ...blockerEN };
export const zhCNMessages = { ...zhCN, ...blockerZH };
// This operator guide is maintained in English and Simplified Chinese only.
// Share its English fallback explicitly; do not add placeholder translations
// or automatically fill unrelated missing keys in other language catalogs.
export const jaMessages = { ...blockerEN, ...ja };
export const koMessages = { ...blockerEN, ...ko };
export const esMessages = { ...blockerEN, ...es };
export const frMessages = { ...blockerEN, ...fr };
export const deMessages = { ...blockerEN, ...de };
export const ptBRMessages = { ...blockerEN, ...ptBR };

export type MessageKey = keyof typeof enMessages;

type PluralCategory = "zero" | "one" | "two" | "few" | "many" | "other";
export type PluralMessageKey = MessageKey extends infer Key extends string
	? Key extends `${infer Base}_${PluralCategory}`
		? Base
		: never
	: never;

export type MessageCatalog = Record<MessageKey, string>;

const catalogs: Record<AppLocale, Readonly<Record<string, string>>> = {
	en: enMessages,
	"zh-CN": zhCNMessages,
	ja: jaMessages,
	ko: koMessages,
	es: esMessages,
	fr: frMessages,
	de: deMessages,
	"pt-BR": ptBRMessages,
};

export function catalogFor(locale: AppLocale): Readonly<Record<string, string>> {
	return catalogs[locale] ?? catalogs.en;
}
