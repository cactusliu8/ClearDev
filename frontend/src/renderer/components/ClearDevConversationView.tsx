import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft } from "lucide-react";
import { useConversation } from "../hooks/useConversation";
import { ChatWorkspace } from "./chat/ChatWorkspace";
import { SessionFilesView } from "./SessionFilesView";
import { Button } from "./ui/button";

export function ClearDevConversationView({ sessionId, title, onBack }: { sessionId: string; title: string; onBack: () => void }) {
	const { t } = useTranslation();
	const conversation = useConversation(sessionId);
	const [tab, setTab] = useState<"conversation" | "files">("conversation");
	return <div className="flex h-full min-h-0 flex-col bg-background" data-testid="cleardev-readonly-session">
		<div className="flex shrink-0 items-center gap-3 border-b px-4 py-2">
			<Button type="button" variant="outline" size="sm" onClick={onBack}><ArrowLeft className="mr-1 size-4" />{t("cleardevConversation.back")}</Button>
			<div className="min-w-0"><p className="truncate text-sm font-medium">{title}</p><p className="text-xs text-muted-foreground">{t("cleardevConversation.readOnly")}</p></div>
			<div className="ml-auto flex gap-1"><Button type="button" size="sm" variant={tab === "conversation" ? "secondary" : "ghost"} onClick={() => setTab("conversation")}>{t("cleardevConversation.conversation")}</Button><Button type="button" size="sm" variant={tab === "files" ? "secondary" : "ghost"} onClick={() => setTab("files")}>{t("cleardevConversation.files")}</Button></div>
		</div>
		{tab === "files" ? <div className="min-h-0 flex-1"><SessionFilesView sessionId={sessionId} readOnly /></div> : <>
			{conversation.isLoading ? <p className="p-4 text-sm text-muted-foreground">{t("cleardevConversation.loading")}</p> : null}
			{conversation.unavailable ? <p className="p-4 text-sm" role="status">{t("cleardevConversation.unavailable", { error: conversation.unavailable.message })}</p> : null}
			{conversation.error ? <p className="p-4 text-sm" role="alert">{conversation.error}</p> : null}
			{conversation.snapshot ? <div className="min-h-0 flex-1"><ChatWorkspace readOnly snapshot={conversation.snapshot} sessionTitle={title} hasOlder={conversation.hasOlder} loadingOlder={conversation.isLoadingOlder} onLoadOlder={conversation.loadOlder} /></div> : null}
		</>}
	</div>;
}
