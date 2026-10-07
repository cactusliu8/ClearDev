import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	cleardevProgressQueryKey,
	fetchClearDevProjectProgress,
	requestClearDevProgressExplanation,
} from "../lib/cleardev-progress";
import { hasTrustedApiBaseUrl } from "../lib/api-client";

export function useClearDevProgressQuery(projectId: string) {
	return useQuery({
		queryKey: cleardevProgressQueryKey(projectId),
		queryFn: () => fetchClearDevProjectProgress(projectId),
		enabled: Boolean(projectId) && hasTrustedApiBaseUrl(),
		retry: 1,
		refetchInterval: 5000,
	});
}

export function useRequestClearDevProgressExplanation(projectId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (requirementId: string) => requestClearDevProgressExplanation(requirementId),
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: cleardevProgressQueryKey(projectId) });
		},
	});
}
