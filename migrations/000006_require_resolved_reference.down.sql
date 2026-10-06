ALTER TABLE wager_transactions DROP CONSTRAINT wt_processed_reference_is_resolved_ck;

ALTER TABLE wager_transactions ADD CONSTRAINT wt_processed_reversal_has_resolved_reference_ck CHECK (
    status <> 'PROCESSED' OR kind NOT IN ('REFUND', 'ROLLBACK') OR reference_transaction_id IS NOT NULL);
