-- +goose Up

-- The client's own name for the application it builds, from OpenRouter's app-attribution headers: the URL in
-- HTTP-Referer, the name in X-OpenRouter-Title (or the older X-Title) and the marketplace categories in
-- X-OpenRouter-Categories. They are header text, so each is bounded here as well as at the gateway: a header longer than
-- its column must not make the database refuse the whole call record. Empty means the client named none.
ALTER TABLE observability.call_log
    ADD COLUMN app_referer text NOT NULL DEFAULT '' CHECK (char_length(app_referer) <= 2048),
    ADD COLUMN app_title text NOT NULL DEFAULT '' CHECK (char_length(app_title) <= 200),
    ADD COLUMN app_categories text NOT NULL DEFAULT '' CHECK (char_length(app_categories) <= 64);
