package quality

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The benchmark tables are the shared record behind the product score (docs/domains/quality.md「产品成绩（固定测评题）」).
// Baselines and scores carry the benchmark version and hash, and only the running binary's own pair is read: a newer
// set's rows do not disturb an older node still serving its set, and an older set's rows can never be mistaken for the
// current one. One baseline per model and version; one score per endpoint and version. benchmark_configs carries each
// endpoint configuration's generation, the invalidation marker shared by every node.
const (
	baselineColumns = `model_id,format,endpoint_id,run_at,scores`
	configColumns   = `endpoint_id,model_id,upstream,formats,epoch`
	scoreColumns    = `endpoint_id,model_id,upstream,formats,score,confidence,run_at,baseline_at,epoch`
)

// benchmarkEvery is how often a node rereads the stored baselines and scores. The routing snapshot's own rebuild then
// carries them within about five seconds, without a restart (docs/domains/quality.md「产品成绩（固定测评题）」).
const benchmarkEvery = 5 * time.Second

// savedScore is the stored score for one endpoint, without taking the endpoint lock.
func (m *Monitor) savedScore(id suppliers.EndpointID) (benchmarkState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	score, ok := m.scores[id]
	return score, ok
}

// storedConfig is the last configuration generation this node read for one endpoint.
func (m *Monitor) storedConfig(id suppliers.EndpointID) (benchmarkConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	config, ok := m.configs[id]
	return config, ok
}

// adoptBenchmarks merges a read of the store into the node's view. Baselines keep the newer row: a local caller may
// already have published a newest one, and a refresh must never move the node backwards. Scores and configurations are
// replaced whole: the store deleted the scores of a replaced configuration, so a cached row missing from the read must
// not stay eligible.
func (m *Monitor) adoptBenchmarks(anchors map[uuid.UUID]benchmarkAnchor, scores map[suppliers.EndpointID]benchmarkState, configs map[suppliers.EndpointID]benchmarkConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for model, anchor := range anchors {
		if old, ok := m.benchmarks[model]; !ok || anchor.at.After(old.at) {
			m.benchmarks[model] = anchor
		}
	}
	m.scores = scores
	m.configs = configs
}

// ensureConfig records ep's configuration and returns its generation. A changed configuration advances the generation
// and deletes the scores of the configuration it replaces in one transaction, so a write that raced the change finds
// no matching configuration and cannot leave an old-configuration row behind. The caller learns whether the store
// changed, letting it drop a cached score only when the store did.
func (m *Monitor) ensureConfig(ctx context.Context, ep suppliers.Routable) (int64, error) {
	if m.loop == nil || m.loop.db == nil {
		return 0, nil
	}
	epoch, changed, err := m.loop.ensureConfig(ctx, ep)
	if err != nil {
		m.loop.logger.Error("quality: record the endpoint benchmark configuration", "endpoint", ep.ID.String(), "err", err)
		return 0, err
	}
	m.mu.Lock()
	if changed {
		delete(m.scores, ep.ID)
	}
	m.configs[ep.ID] = benchmarkConfig{config: configOf(ep), epoch: epoch}
	m.mu.Unlock()
	return epoch, nil
}

// configForScore records a configuration that has no row yet and returns the stored generation. It never changes a row
// that already names another configuration: a score written after Prepare moved the endpoint must lose at the write,
// not be rescued by recreating its old configuration. Without a database the in-memory record is the whole record.
func (m *Monitor) configForScore(ctx context.Context, ep suppliers.Routable) bool {
	if m.loop == nil || m.loop.db == nil {
		return true
	}
	epoch, ok, err := m.loop.configForScore(ctx, ep)
	if err != nil {
		m.loop.logger.Error("quality: record the endpoint benchmark configuration", "endpoint", ep.ID.String(), "err", err)
		return false
	}
	if ok {
		m.mu.Lock()
		m.configs[ep.ID] = benchmarkConfig{config: configOf(ep), epoch: epoch}
		m.mu.Unlock()
	}
	return ok
}

// storeBaseline writes an official anchor and the official endpoint's own 100 before either is published. Without a
// database (unit tests, an isolated monitor) there is nowhere to persist and the in-memory record is the whole record.
// The returned generation is the one the score was stored under.
func (m *Monitor) storeBaseline(ctx context.Context, ep suppliers.Routable, anchor benchmarkAnchor, state benchmarkState) (bool, int64) {
	if m.loop == nil || m.loop.db == nil {
		return true, 0
	}
	won, epoch, err := m.loop.saveBaseline(ctx, ep, anchor, state)
	if err != nil {
		m.loop.logger.Error("quality: persist the official benchmark baseline", "model", ep.Model.String(), "err", err)
		return false, 0
	}
	return won, epoch
}

// storeScore writes a scored run before it is published. The returned generation is the one the score was stored under.
func (m *Monitor) storeScore(ctx context.Context, ep suppliers.Routable, state benchmarkState) (bool, int64) {
	if m.loop == nil || m.loop.db == nil {
		return true, 0
	}
	won, epoch, err := m.loop.saveScore(ctx, ep, state)
	if err != nil {
		m.loop.logger.Error("quality: persist a benchmark score", "endpoint", ep.ID.String(), "model", ep.Model.String(), "err", err)
		return false, 0
	}
	return won, epoch
}

// saveBaseline writes the anchor and its official score atomically: either the new anchor has its 100 everywhere, or
// neither is stored and the previous anchor stays in force.
func (l *Loop) saveBaseline(ctx context.Context, ep suppliers.Routable, anchor benchmarkAnchor, state benchmarkState) (bool, int64, error) {
	var won bool
	var epoch int64
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		var err error
		won, err = writeBenchmarkBaseline(ctx, tx, ep.Model, anchor)
		if err != nil || !won {
			return err
		}
		var scored bool
		epoch, scored, err = writeBenchmarkScore(ctx, tx, ep, state)
		if err != nil {
			return err
		}
		if !scored {
			return fmt.Errorf("quality: the official score lost to a newer row after its baseline won")
		}
		return nil
	})
	return won, epoch, err
}

func (l *Loop) saveScore(ctx context.Context, ep suppliers.Routable, state benchmarkState) (bool, int64, error) {
	var won bool
	var epoch int64
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		var err error
		epoch, won, err = writeBenchmarkScore(ctx, tx, ep, state)
		return err
	})
	return won, epoch, err
}

// ensureConfig is the store half of Monitor.ensureConfig. The insert-or-update is a compare-and-swap on the stored
// configuration: a matching row is left alone, a different one gets the next generation. A returned row means the
// configuration changed, so the old scores are deleted before the transaction commits.
func (l *Loop) ensureConfig(ctx context.Context, ep suppliers.Routable) (epoch int64, changed bool, err error) {
	err = l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		cfg := configOf(ep)
		scan := tx.QueryRow(ctx, `INSERT INTO quality.benchmark_configs
 (endpoint_id,model_id,upstream,formats,epoch) VALUES ($1,$2,$3,$4,1)
 ON CONFLICT (endpoint_id) DO UPDATE SET model_id=excluded.model_id,upstream=excluded.upstream,
 formats=excluded.formats,epoch=benchmark_configs.epoch+1,updated_at=now()
 WHERE (benchmark_configs.model_id,benchmark_configs.upstream,benchmark_configs.formats)
       IS DISTINCT FROM (excluded.model_id,excluded.upstream,excluded.formats)
 RETURNING epoch`, ep.ID, cfg.Model, cfg.Upstream, int32(cfg.Formats))
		err := scan.Scan(&epoch)
		if errors.Is(err, pg.ErrNoRows) {
			return tx.QueryRow(ctx, `SELECT epoch FROM quality.benchmark_configs WHERE endpoint_id=$1`, ep.ID).Scan(&epoch)
		}
		if err != nil {
			return err
		}
		changed = true
		_, err = tx.Exec(ctx, `DELETE FROM quality.benchmark_scores WHERE endpoint_id=$1`, ep.ID)
		return err
	})
	return epoch, changed, err
}

// configForScore is the insert-if-absent half of configuration recording. An existing row is read and returned only if
// it names the same configuration; a row for another configuration is left untouched, so the score write that follows
// cannot attach to it.
func (l *Loop) configForScore(ctx context.Context, ep suppliers.Routable) (epoch int64, ok bool, err error) {
	cfg := configOf(ep)
	err = l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO quality.benchmark_configs
 (endpoint_id,model_id,upstream,formats,epoch) VALUES ($1,$2,$3,$4,1)
 ON CONFLICT (endpoint_id) DO NOTHING RETURNING epoch`,
			ep.ID, cfg.Model, cfg.Upstream, int32(cfg.Formats)).Scan(&epoch)
		if err == nil {
			ok = true
			return nil
		}
		if !errors.Is(err, pg.ErrNoRows) {
			return err
		}
		var model uuid.UUID
		var upstream string
		var formats int32
		err = tx.QueryRow(ctx, `SELECT model_id,upstream,formats,epoch
 FROM quality.benchmark_configs WHERE endpoint_id=$1`, ep.ID).Scan(&model, &upstream, &formats, &epoch)
		if errors.Is(err, pg.ErrNoRows) {
			return fmt.Errorf("quality: benchmark configuration for %s disappeared", ep.ID)
		}
		ok = err == nil && model == cfg.Model && upstream == cfg.Upstream && formats == int32(cfg.Formats)
		return err
	})
	return epoch, ok, err
}

// writeBenchmarkBaseline stores the official anchor for a model and version. Only a newer run replaces the same
// version's anchor; rows of another version are separate, so a rolling upgrade keeps each node's baseline.
func writeBenchmarkBaseline(ctx context.Context, tx pg.Tx, model uuid.UUID, anchor benchmarkAnchor) (bool, error) {
	scores, err := jsonx.MarshalCanonical(anchor.scores)
	if err != nil {
		return false, fmt.Errorf("quality: encode the benchmark baseline: %w", err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO quality.benchmark_baselines
 (model_id,version,hash,format,endpoint_id,run_at,scores) VALUES ($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT (model_id,version) DO UPDATE SET hash=excluded.hash,format=excluded.format,
 endpoint_id=excluded.endpoint_id,run_at=excluded.run_at,scores=excluded.scores,updated_at=now()
 WHERE excluded.run_at>benchmark_baselines.run_at`,
		model, anchor.version, anchor.hash, int16(anchor.format), anchor.endpoint, anchor.at.UnixNano(), scores)
	return err == nil && tag.RowsAffected() == 1, err
}

// writeBenchmarkScore stores a scored run. The INSERT only fires while the baseline it was scored against is still the
// stored anchor and the endpoint's stored configuration still matches the score, so a baseline replaced or a
// configuration changed by another node between this node's read and write cannot be given a score it never produced.
// The row takes the configuration's current generation; the conflict clause keeps an older run from replacing a newer
// one. A zero row count means the run lost and nothing was written.
func writeBenchmarkScore(ctx context.Context, tx pg.Tx, ep suppliers.Routable, state benchmarkState) (int64, bool, error) {
	var epoch int64
	err := tx.QueryRow(ctx, `WITH current AS (
   SELECT model_id,upstream,formats,epoch FROM quality.benchmark_configs
   WHERE endpoint_id=$1 AND model_id=$4 AND upstream=$5 AND formats=$6
   FOR SHARE
 )
 INSERT INTO quality.benchmark_scores
 (endpoint_id,version,hash,model_id,upstream,formats,score,confidence,run_at,baseline_at,epoch)
 SELECT $1,$2,$3,c.model_id,c.upstream,c.formats,$7,$8,$9,$10,c.epoch
 FROM current c
 WHERE EXISTS (SELECT 1 FROM quality.benchmark_baselines
   WHERE model_id=$4 AND version=$2 AND hash=$3 AND run_at=$10)
 ON CONFLICT (endpoint_id,version) DO UPDATE SET hash=excluded.hash,model_id=excluded.model_id,
 upstream=excluded.upstream,formats=excluded.formats,score=excluded.score,confidence=excluded.confidence,
 run_at=excluded.run_at,baseline_at=excluded.baseline_at,epoch=excluded.epoch,updated_at=now()
 WHERE excluded.baseline_at>benchmark_scores.baseline_at
 OR (excluded.baseline_at=benchmark_scores.baseline_at AND excluded.run_at>benchmark_scores.run_at)
 RETURNING epoch`,
		ep.ID, state.version, state.hash, state.config.Model, state.config.Upstream, int32(state.config.Formats),
		state.score, state.confidence, state.at.UnixNano(), state.baseline.UnixNano()).Scan(&epoch)
	if errors.Is(err, pg.ErrNoRows) {
		return 0, false, nil
	}
	return epoch, err == nil, err
}

// loadBenchmarks reads the stored baselines, scores and configuration generations before the node serves traffic. A
// table that cannot be read stops the start, like the other persisted state quality owns.
func (l *Loop) loadBenchmarks(ctx context.Context, now time.Time) error {
	l.benchmarksAt = now
	if l.db == nil {
		return nil
	}
	anchors, scores, configs, err := l.readBenchmarks(ctx)
	if err != nil {
		return fmt.Errorf("quality: read the benchmark scores: %w", err)
	}
	l.monitor.adoptBenchmarks(anchors, scores, configs)
	return nil
}

// refreshBenchmarks rereads the store every benchmarkEvery. A failed read keeps the previous values and is logged:
// stored scores are evidence for routing, and a transient database error must not stop the node.
func (l *Loop) refreshBenchmarks(ctx context.Context, now time.Time) {
	if l.db == nil || now.Sub(l.benchmarksAt) < benchmarkEvery {
		return
	}
	l.benchmarksAt = now
	anchors, scores, configs, err := l.readBenchmarks(ctx)
	if err != nil {
		l.logger.Error("quality: reload the benchmark scores; the previous values stay in force", "err", err)
		return
	}
	l.monitor.adoptBenchmarks(anchors, scores, configs)
}

// readBenchmarks reads the current set's baselines, scores and configuration generations as one consistent state. A row
// this binary cannot use — another format, an unknown question, a fraction outside 0..1 — is dropped and logged: it can
// only come from a broken writer, and withholding one model's score is safer than scoring against it.
func (l *Loop) readBenchmarks(ctx context.Context) (map[uuid.UUID]benchmarkAnchor, map[suppliers.EndpointID]benchmarkState, map[suppliers.EndpointID]benchmarkConfig, error) {
	set := fixedBenchmark()
	anchors := make(map[uuid.UUID]benchmarkAnchor)
	scores := make(map[suppliers.EndpointID]benchmarkState)
	configs := make(map[suppliers.EndpointID]benchmarkConfig)
	err := l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) error {
		if err := l.readBaselines(ctx, tx, set, anchors); err != nil {
			return err
		}
		if err := l.readConfigs(ctx, tx, configs); err != nil {
			return err
		}
		return l.readScores(ctx, tx, set, scores)
	})
	return anchors, scores, configs, err
}

// readConfigs loads every endpoint's current configuration generation. The rows are not tied to a benchmark version:
// a configuration change invalidates the endpoint's scores in every set.
func (l *Loop) readConfigs(ctx context.Context, tx pg.Tx, out map[suppliers.EndpointID]benchmarkConfig) error {
	rows, err := tx.Query(ctx, `SELECT `+configColumns+` FROM quality.benchmark_configs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, model uuid.UUID
		var upstream string
		var formats int32
		var epoch int64
		if err := rows.Scan(&id, &model, &upstream, &formats, &epoch); err != nil {
			return err
		}
		if id == (uuid.UUID{}) || model == (uuid.UUID{}) || formats < 0 || formats > 0x3f || epoch <= 0 {
			l.logger.Warn("quality: ignoring an unusable stored benchmark configuration", "endpoint", id.String())
			continue
		}
		out[id] = benchmarkConfig{
			config: EndpointConfig{Model: model, Upstream: upstream, Formats: benchmarkFormatsOf(formats)},
			epoch:  epoch,
		}
	}
	return rows.Err()
}

// readBaselines loads the current set's anchors, dropping a row this binary cannot use.
func (l *Loop) readBaselines(ctx context.Context, tx pg.Tx, set *BenchmarkSet, out map[uuid.UUID]benchmarkAnchor) error {
	rows, err := tx.Query(ctx, `SELECT `+baselineColumns+` FROM quality.benchmark_baselines
 WHERE version=$1 AND hash=$2`, set.Version, set.Hash)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model uuid.UUID
		var format int16
		var endpoint uuid.UUID
		var at int64
		var raw []byte
		if err := rows.Scan(&model, &format, &endpoint, &at, &raw); err != nil {
			return err
		}
		var points map[string]float64
		if at <= 0 {
			l.logger.Warn("quality: ignoring a stored benchmark baseline without a run time", "model", model.String())
			continue
		}
		if err := jsonx.Unmarshal(raw, &points); err != nil {
			l.logger.Warn("quality: ignoring an undecodable stored benchmark baseline", "model", model.String(), "err", err)
			continue
		}
		anchor := benchmarkAnchor{
			version: set.Version, hash: set.Hash, format: benchmarkFormatOf(format), endpoint: endpoint,
			at: time.Unix(0, at).UTC(), scores: points,
		}
		if model == (uuid.UUID{}) || endpoint == (uuid.UUID{}) || !anchor.valid(set) {
			l.logger.Warn("quality: ignoring an unusable stored benchmark baseline", "model", model.String())
			continue
		}
		out[model] = anchor
	}
	return rows.Err()
}

// readScores loads the current set's endpoint scores, dropping a row this binary cannot use.
func (l *Loop) readScores(ctx context.Context, tx pg.Tx, set *BenchmarkSet, out map[suppliers.EndpointID]benchmarkState) error {
	rows, err := tx.Query(ctx, `SELECT `+scoreColumns+` FROM quality.benchmark_scores
 WHERE version=$1 AND hash=$2`, set.Version, set.Hash)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, model uuid.UUID
		var upstream string
		var formats int32
		var score, confidence float64
		var at, baseline, epoch int64
		if err := rows.Scan(&id, &model, &upstream, &formats, &score, &confidence, &at, &baseline, &epoch); err != nil {
			return err
		}
		if !benchmarkScoreUsable(id, model, formats, score, confidence, at, baseline, epoch) {
			l.logger.Warn("quality: ignoring an unusable stored benchmark score", "endpoint", id.String())
			continue
		}
		out[id] = benchmarkState{
			version: set.Version, hash: set.Hash,
			baseline: time.Unix(0, baseline).UTC(), score: score, confidence: confidence,
			at:     time.Unix(0, at).UTC(),
			config: EndpointConfig{Model: model, Upstream: upstream, Formats: benchmarkFormatsOf(formats)},
			epoch:  epoch,
		}
	}
	return rows.Err()
}

// benchmarkFormatOf turns a stored smallint into a format, or into a value valid rejects when the column is outside
// the closed set. The explicit range check keeps the narrowing conversion safe.
func benchmarkFormatOf(v int16) protocol.Format {
	if v < 0 || v > int16(protocol.Completions) {
		return protocol.Format(255)
	}
	return protocol.Format(v)
}

// benchmarkFormatsOf turns a stored integer into the format bitmask; the range is checked again on read, and the
// conversion cannot truncate a value benchmarkScoreUsable accepted.
func benchmarkFormatsOf(v int32) uint16 {
	if v < 0 || v > 0x3f {
		return 0
	}
	return uint16(v)
}

// benchmarkScoreUsable rejects a stored score outside the columns' checks: a broken writer, not a value to score
// endpoints against.
func benchmarkScoreUsable(id, model uuid.UUID, formats int32, score, confidence float64, at, baseline, epoch int64) bool {
	return id != (uuid.UUID{}) && model != (uuid.UUID{}) && formats >= 0 && formats <= 0x3f &&
		at > 0 && baseline > 0 && score >= 0 && score <= 100 && confidence >= 0 && confidence <= 1 && epoch > 0
}
