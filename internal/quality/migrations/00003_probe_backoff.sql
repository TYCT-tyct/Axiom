-- +goose Up

-- The first probe delay can be as short as one second, so the backoff needs ten doublings to reach the ten-minute cap
-- (1, 2, 4, ... 512, 600). The old ceiling of five assumed the fixed thirty-second default and would stop at 32 seconds.
ALTER TABLE quality.endpoint_health DROP CONSTRAINT endpoint_health_backoff_check;
ALTER TABLE quality.endpoint_health ADD CONSTRAINT endpoint_health_backoff_check CHECK (backoff BETWEEN 0 AND 10);

-- +goose Down
ALTER TABLE quality.endpoint_health DROP CONSTRAINT endpoint_health_backoff_check;
ALTER TABLE quality.endpoint_health ADD CONSTRAINT endpoint_health_backoff_check CHECK (backoff BETWEEN 0 AND 5);
