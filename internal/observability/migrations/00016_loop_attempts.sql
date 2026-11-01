-- +goose Up

-- A call logs one attempt per supplier request, and a model-side loop makes one per sampling: the number runs to
-- platform.MaxAttemptSeq, 33 (suppliers' migration 00024 widens its own the same way). Only widening, so what an old
-- node writes still fits; NOT VALID because scanning every partition under ACCESS EXCLUSIVE would block the recorder,
-- and the rows already there fit the narrower check.
ALTER TABLE observability.call_attempt DROP CONSTRAINT call_attempt_attempt_seq_check,
    ADD CONSTRAINT call_attempt_attempt_seq_check CHECK (attempt_seq BETWEEN 1 AND 33) NOT VALID;

-- +goose Down

ALTER TABLE observability.call_attempt DROP CONSTRAINT call_attempt_attempt_seq_check,
    ADD CONSTRAINT call_attempt_attempt_seq_check CHECK (attempt_seq BETWEEN 1 AND 3) NOT VALID;
