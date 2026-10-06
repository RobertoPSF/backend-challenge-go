ALTER TABLE wager_transactions DROP CONSTRAINT wt_processed_reversal_has_resolved_reference_ck;

ALTER TABLE wager_transactions ADD CONSTRAINT wt_processed_reference_is_resolved_ck CHECK (
    status <> 'PROCESSED' OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL);
