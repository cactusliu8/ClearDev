import type { components } from "../../api/schema";
import { apiClient, hasTrustedApiBaseUrl } from "./api-client";

export const cleardevProgressQueryRoot = ["cleardev-progress"] as const;

export function cleardevProgressQueryKey(projectId: string) {
	return ["cleardev-progress", projectId] as const;
}

export type ClearDevProjectProgressView = components["schemas"]["ClearDevProjectProgressView"];
export type ClearDevTrustedProgressSummary = components["schemas"]["ClearDevTrustedProgressSummary"];

export async function fetchClearDevProjectProgress(projectId: string): Promise<ClearDevProjectProgressView> {
	if (!hasTrustedApiBaseUrl()) {
		throw new Error("AO daemon API is not ready");
	}
	const { data, error } = await apiClient.GET("/api/v1/cleardev/projects/{projectId}/progress", {
		params: { path: { projectId } },
	});
	if (error) throw error;
	if (!data) throw new Error("ClearDev progress was empty");
	return data;
}

export async function requestClearDevProgressExplanation(requirementId: string): Promise<unknown> {
	if (!hasTrustedApiBaseUrl()) {
		throw new Error("AO daemon API is not ready");
	}
	const { data, error } = await apiClient.POST("/api/v1/cleardev/requirements/{id}/progress-explanations", {
		params: { path: { id: requirementId } },
	});
	if (error) throw error;
	return data;
}
