-- +goose Up

-- The client's own name for the application it builds, from OpenRouter's app-attribution headers: the URL in
-- HTTP-Referer, the name in X-OpenRouter-Title (or the older X-Title) and the marketplace categories in
-- X-OpenRouter-Categories. They are header text, so each is bounded here as well as at the gateway: a header longer than
-- its column must not make the database refuse the whole call record. Empty means the client named none.
ALTER TABLE observability.call_log
    ADD COLUMN app_referer text NOT NULL DEFAULT '',
    ADD COLUMN app_title text NOT NULL DEFAULT '',
    ADD COLUMN app_categories text NOT NULL DEFAULT '';

-- Adding a CHECK beside the column would scan every partition while the ALTER holds ACCESS EXCLUSIVE, blocking the
-- recorder and the console for as long as the biggest partition takes. NOT VALID checks new rows and leaves the
-- existing ones (all '') to a later VALIDATE, which does not block writes.
ALTER TABLE observability.call_log
    ADD CONSTRAINT call_log_app_referer_check CHECK (char_length(app_referer) <= 2048) NOT VALID,
    ADD CONSTRAINT call_log_app_title_check CHECK (char_length(app_title) <= 200) NOT VALID,
    ADD CONSTRAINT call_log_app_categories_check CHECK (char_length(app_categories) <= 64) NOT VALID;
