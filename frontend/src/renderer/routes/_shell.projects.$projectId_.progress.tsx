import { createFileRoute } from "@tanstack/react-router";
import { ClearDevProgressPage } from "../components/ClearDevProgressPage";

export const Route = createFileRoute("/_shell/projects/$projectId_/progress")({
	component: ClearDevProgressRoute,
});

function ClearDevProgressRoute() {
	const { projectId } = Route.useParams();
	return <ClearDevProgressPage projectId={projectId} />;
}
