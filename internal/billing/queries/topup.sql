-- name: TopUpBuffer :many
SELECT ppm FROM billing.top_up_buffers WHERE site_id = @site_id;

-- name: SetTopUpBuffer :exec
INSERT INTO billing.top_up_buffers (site_id, ppm) VALUES (@site_id, @ppm)
ON CONFLICT (site_id) DO UPDATE SET ppm = excluded.ppm;
