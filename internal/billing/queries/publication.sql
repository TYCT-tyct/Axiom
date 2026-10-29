-- name: EnsurePricePolicy :exec
INSERT INTO billing.price_policies(site_id) VALUES (@site_id) ON CONFLICT DO NOTHING;

-- name: LockPricePolicy :one
SELECT rev,floor_multiplier,change_ratio,max_increase_ratio FROM billing.price_policies WHERE site_id= @site_id FOR UPDATE;

-- name: UpdatePricePolicy :exec
UPDATE billing.price_policies SET rev=rev+1,floor_multiplier= @floor_multiplier,change_ratio= @change_ratio,max_increase_ratio= @max_increase_ratio WHERE site_id= @site_id;

-- name: ReadPublications :many
SELECT p.model_id,p.candidate_id,p.candidate,p.current_id,p.status,v.schedule,v.basis,v.overridden,v.candidate_id AS published_candidate
FROM billing.price_publications p LEFT JOIN billing.price_versions v ON v.id=p.current_id WHERE p.site_id= @site_id ORDER BY p.model_id;

-- name: SavePublication :exec
INSERT INTO billing.price_publications(site_id,model_id,candidate_id,candidate,current_id,status)
VALUES(@site_id,@model_id,@candidate_id,@candidate,sqlc.narg(current_id),@status)
ON CONFLICT(site_id,model_id) DO UPDATE SET candidate_id=excluded.candidate_id,candidate=excluded.candidate,current_id=excluded.current_id,status=excluded.status;

-- name: InsertPriceVersion :exec
INSERT INTO billing.price_versions(id,site_id,model_id,candidate_id,schedule,basis,overridden,actor,reason)
VALUES(@id,@site_id,@model_id,@candidate_id,@schedule,@basis,@overridden,@actor,@reason);
