package store

import "context"

// IsClearDevControlledSession checks durable bindings, including ended roles.
// A historical Worker remains controlled so AO's generic session actions
// cannot bypass the ClearDev transcript and budget rules after a handoff.
func (s *Store) IsClearDevControlledSession(ctx context.Context, sessionID string) (bool, error) {
	const query = `SELECT EXISTS(
		SELECT 1 FROM cleardev_complex_role_bindings WHERE ao_session_id=?
		UNION ALL SELECT 1 FROM cleardev_standard_role_bindings WHERE ao_session_id=?
		UNION ALL SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE ao_session_id=?
		UNION ALL SELECT 1 FROM cleardev_complex_quick_role_bindings WHERE ao_session_id=?
		UNION ALL SELECT 1 FROM cleardev_complex_exception_ondemand_bindings WHERE ao_session_id=?
		UNION ALL SELECT 1 FROM cleardev_requirement_final_reviews WHERE ao_session_id=?
		UNION ALL SELECT 1 FROM cleardev_agent_step_attempts WHERE ao_session_id=?
		UNION ALL SELECT 1 FROM cleardev_progress_explanation_requests WHERE ao_session_id=?
		-- A session seed can be live before its role binding has the AO id.
		-- The stored creation key is the same exact binding identity.
		UNION ALL SELECT 1 FROM sessions s JOIN cleardev_complex_role_bindings b ON b.session_creation_idempotency_key=s.creation_idempotency_key WHERE s.id=?
		UNION ALL SELECT 1 FROM sessions s JOIN cleardev_standard_role_bindings b ON b.session_creation_idempotency_key=s.creation_idempotency_key WHERE s.id=?
		UNION ALL SELECT 1 FROM sessions s JOIN cleardev_complex_execution_role_bindings b ON b.session_creation_idempotency_key=s.creation_idempotency_key WHERE s.id=?
		UNION ALL SELECT 1 FROM sessions s JOIN cleardev_complex_quick_role_bindings b ON b.session_creation_idempotency_key=s.creation_idempotency_key WHERE s.id=?
		UNION ALL SELECT 1 FROM sessions s JOIN cleardev_complex_exception_ondemand_bindings b ON b.session_creation_idempotency_key=s.creation_idempotency_key WHERE s.id=?
		UNION ALL SELECT 1 FROM sessions s JOIN cleardev_progress_explanation_requests r ON r.session_creation_idempotency_key=s.creation_idempotency_key WHERE s.id=?
		UNION ALL SELECT 1 FROM sessions s JOIN cleardev_requirement_final_reviews r ON
			s.creation_idempotency_key='cleardev-requirement-final-review:'||COALESCE(json_extract(r.review_packet_json,'$.previousReview.reviewId'),r.id)
			WHERE s.id=?
	)`
	var controlled bool
	err := s.readDB.QueryRowContext(ctx, query,
		sessionID, sessionID, sessionID, sessionID, sessionID, sessionID, sessionID, sessionID,
		sessionID, sessionID, sessionID, sessionID, sessionID, sessionID, sessionID).Scan(&controlled)
	return controlled, err
}
