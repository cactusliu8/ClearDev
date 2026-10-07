import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { ClipboardList } from "lucide-react";
import { TopbarButton } from "./TopbarButton";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

export function ClearDevProgressButton({ projectId }: { projectId: string }) {
	const { t } = useTranslation();
	const navigate = useNavigate();

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="inline-flex">
					<TopbarButton
						aria-label={t("cleardevProgress.open")}
						className="topbar-control--labeled"
						data-priority="secondary"
						data-testid="cleardev-progress-open"
						onClick={() =>
							void navigate({
								to: "/projects/$projectId/progress",
								params: { projectId },
							})
						}
						variant="accent"
					>
						<ClipboardList className="size-icon-md" aria-hidden="true" />
						<span data-compact-label>{t("cleardevProgress.open")}</span>
					</TopbarButton>
				</span>
			</TooltipTrigger>
			<TooltipContent side="bottom">{t("cleardevProgress.open")}</TooltipContent>
		</Tooltip>
	);
}
