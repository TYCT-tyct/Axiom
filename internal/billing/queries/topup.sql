-- name: TopUpBuffer :many
SELECT ppm FROM billing.top_up_buffers WHERE site_id = @site_id;
