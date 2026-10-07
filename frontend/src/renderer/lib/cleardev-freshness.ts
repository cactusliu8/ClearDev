import { useEffect, useState } from "react";

const maxFactAgeMs = 30_000;

// A clock tick makes cached facts age out even when the connection is offline
// and no query result arrives to trigger a render.
export function useFreshnessClock(): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		const timer = setInterval(() => setNow(Date.now()), 5_000);
		return () => clearInterval(timer);
	}, []);
	return now;
}

export function hasCurrentFacts(query: { isSuccess: boolean; isError: boolean; dataUpdatedAt: number }, now: number): boolean {
	return query.isSuccess && !query.isError && query.dataUpdatedAt > 0 && now - query.dataUpdatedAt <= maxFactAgeMs;
}
