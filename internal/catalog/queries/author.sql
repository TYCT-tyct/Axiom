-- name: GetAuthor :one
SELECT * FROM catalog.author WHERE slug = @slug;

-- name: InsertAuthor :exec
INSERT INTO catalog.author (slug, name) VALUES (@slug, @name);

-- name: RenameAuthor :exec
UPDATE catalog.author SET name = @name WHERE slug = @slug;
