-- +goose Up

-- An open registration keeps the inviter its link named until the address is verified, when the user is created with
-- it (docs/domains/accounts.md「邀请关系」). A repeated request replaces it with the new request's, like the rest of
-- the row. The inviter is a user of the same site; users are never deleted, so the request cannot outlive them.
ALTER TABLE accounts.registration_requests ADD COLUMN inviter_user_id uuid,
    ADD CONSTRAINT registration_requests_site_id_inviter_user_id_fkey FOREIGN KEY (site_id, inviter_user_id)
        REFERENCES accounts.users (site_id, id);
GRANT UPDATE(inviter_user_id) ON TABLE accounts.registration_requests TO axiom_app;
