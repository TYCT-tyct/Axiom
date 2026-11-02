-- name: EnsurePricePolicy :exec
INSERT INTO billing.price_policies(site_id,floor_multiplier,change_ratio,max_increase_ratio) VALUES (@site_id,@floor_multiplier,@change_ratio,@max_increase_ratio) ON CONFLICT DO NOTHING;

-- name: LockPricePolicy :one
SELECT rev,floor_multiplier,change_ratio,max_increase_ratio,byok_fee_bps FROM billing.price_policies WHERE site_id= @site_id FOR UPDATE;

-- name: UpdatePricePolicy :exec
UPDATE billing.price_policies SET rev=rev+1,floor_multiplier= @floor_multiplier,change_ratio= @change_ratio,max_increase_ratio= @max_increase_ratio,byok_fee_bps= @byok_fee_bps WHERE site_id= @site_id;

-- name: ReadPublications :many
SELECT p.audience_kind,p.audience_id,p.model_id,p.candidate_id,p.candidate,p.current_id,p.status,v.schedule,v.basis,v.overridden,v.endpoints,v.candidate_id AS published_candidate
FROM (
 SELECT 'all'::text AS audience_kind,''::text AS audience_id,model_id,candidate_id,candidate,current_id,status FROM billing.price_publications d WHERE d.site_id= @site_id
 UNION ALL
 SELECT audience_kind,audience_id,model_id,candidate_id,candidate,current_id,status FROM billing.audience_publications a WHERE a.site_id= @site_id
) p LEFT JOIN billing.price_versions v ON v.id=p.current_id ORDER BY p.model_id,p.audience_kind,p.audience_id;

-- name: SavePublication :exec
INSERT INTO billing.price_publications(site_id,model_id,candidate_id,candidate,current_id,status)
VALUES(@site_id,@model_id,@candidate_id,@candidate,sqlc.narg(current_id),@status)
ON CONFLICT(site_id,model_id) DO UPDATE SET candidate_id=excluded.candidate_id,candidate=excluded.candidate,current_id=excluded.current_id,status=excluded.status;

-- name: SaveAudiencePublication :exec
INSERT INTO billing.audience_publications(site_id,model_id,audience_kind,audience_id,candidate_id,candidate,current_id,status)
VALUES(@site_id,@model_id,@audience_kind,@audience_id,@candidate_id,@candidate,sqlc.narg(current_id),@status)
ON CONFLICT(site_id,model_id,audience_kind,audience_id) DO UPDATE SET candidate_id=excluded.candidate_id,candidate=excluded.candidate,current_id=excluded.current_id,status=excluded.status;

-- name: InsertPriceVersion :exec
INSERT INTO billing.price_versions(id,site_id,model_id,audience_kind,audience_id,candidate_id,schedule,basis,endpoints,overridden,actor,reason)
VALUES(@id,@site_id,@model_id,@audience_kind,@audience_id,@candidate_id,@schedule,@basis,sqlc.narg(endpoints),@overridden,@actor,@reason);

-- name: ReadPricePolicy :many
SELECT rev,floor_multiplier,change_ratio,max_increase_ratio,byok_fee_bps FROM billing.price_policies WHERE site_id= @site_id;

-- name: ConfirmPriceRefresh :exec
UPDATE billing.price_policies SET confirmed_at=clock_timestamp() WHERE site_id = @site_id;

-- name: FreshPricePolicy :many
SELECT site_id FROM billing.price_policies
WHERE site_id = @site_id AND confirmed_at >= clock_timestamp() - sqlc.arg(max_age_seconds)::int * INTERVAL '1 second';
