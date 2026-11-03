package quality

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The benchmark tables are the shared record behind the product score (docs/domains/quality.md「产品成绩（固定测评题）」).
// Both carry the benchmark version and hash, and only the running binary's own pair is read: a newer set's rows do not
// disturb an older node still serving its set, and an older set's rows can never be mistaken for the current one. One
// baseline per model and version; one score per endpoint and version.
const (
	baselineColumns = `model_id,format,endpoint_id,run_at,scores`
	scoreColumns    = `endpoint_id,model_id,upstream,formats,score,confidence,run_at,baseline_at`
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

// adoptBenchmarks merges a read of the store into the node's view. Only a newer run replaces a baseline or a score:
// a local caller may already have published a newest row, and a refresh must never move the node backwards.
func (m *Monitor) adoptBenchmarks(anchors map[uuid.UUID]benchmarkAnchor, scores map[suppliers.EndpointID]benchmarkState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for model, anchor := range anchors {
		if old, ok := m.benchmarks[model]; !ok || anchor.at.After(old.at) {
			m.benchmarks[model] = anchor
		}
	}
	for id, score := range scores {
		if old, ok := m.scores[id]; !ok || newerScore(score, old) {
			m.scores[id] = score
		}
	}
}

// storeBaseline writes an official anchor and the official endpoint's own 100 before either is published. Without a
// database (unit tests, an isolated monitor) there is nowhere to persist and the in-memory record is the whole record.
func (m *Monitor) storeBaseline(ctx context.Context, ep suppliers.Routable, anchor benchmarkAnchor, state benchmarkState) bool {
	if m.loop == nil || m.loop.db == nil {
		return true
	}
	won, err := m.loop.saveBaseline(ctx, ep, anchor, state)
	if err != nil {
		m.loop.logger.Error("quality: persist the official benchmark baseline", "model", ep.Model.String(), "err", err)
		return false
	}
	return won
}

// storeScore writes a scored run before it is published.
func (m *Monitor) storeScore(ctx context.Context, ep suppliers.Routable, state benchmarkState) bool {
	if m.loop == nil || m.loop.db == nil {
		return true
	}
	won, err := m.loop.saveScore(ctx, ep, state)
	if err != nil {
		m.loop.logger.Error("quality: persist a benchmark score", "endpoint", ep.ID.String(), "model", ep.Model.String(), "err", err)
		return false
	}
	return won
}

// saveBaseline writes the anchor and its official score atomically: either the new anchor has its 100 everywhere, or
// neither is stored and the previous anchor stays in force.
func (l *Loop) saveBaseline(ctx context.Context, ep suppliers.Routable, anchor benchmarkAnchor, state benchmarkState) (bool, error) {
	var won bool
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		var err error
		won, err = writeBenchmarkBaseline(ctx, tx, ep.Model, anchor)
		if err != nil || !won {
			return err
		}
		scored, err := writeBenchmarkScore(ctx, tx, ep, state)
		if err != nil {
			return err
		}
		if !scored {
			return fmt.Errorf("quality: the official score lost to a newer row after its baseline won")
		}
		return nil
	})
	return won, err
}

func (l *Loop) saveScore(ctx context.Context, ep suppliers.Routable, state benchmarkState) (bool, error) {
	var won bool
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		var err error
		won, err = writeBenchmarkScore(ctx, tx, ep, state)
		return err
	})
	return won, err
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
// stored anchor, so a baseline replaced by another node between this node's read and write cannot be given a score it
// never produced; the conflict clause keeps an older run from replacing a newer one.
func writeBenchmarkScore(ctx context.Context, tx pg.Tx, ep suppliers.Routable, state benchmarkState) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO quality.benchmark_scores
 (endpoint_id,version,hash,model_id,upstream,formats,score,confidence,run_at,baseline_at)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10
 WHERE EXISTS (SELECT 1 FROM quality.benchmark_baselines
   WHERE model_id=$4 AND version=$2 AND hash=$3 AND run_at=$10)
 ON CONFLICT (endpoint_id,version) DO UPDATE SET hash=excluded.hash,model_id=excluded.model_id,
 upstream=excluded.upstream,formats=excluded.formats,score=excluded.score,confidence=excluded.confidence,
 run_at=excluded.run_at,baseline_at=excluded.baseline_at,updated_at=now()
 WHERE excluded.baseline_at>benchmark_scores.baseline_at
 OR (excluded.baseline_at=benchmark_scores.baseline_at AND excluded.run_at>benchmark_scores.run_at)`,
		ep.ID, state.version, state.hash, state.config.Model, state.config.Upstream, int32(state.config.Formats),
		state.score, state.confidence, state.at.UnixNano(), state.baseline.UnixNano())
	return err == nil && tag.RowsAffected() == 1, err
}

// loadBenchmarks reads the stored baselines and scores before the node serves traffic. A table that cannot be read
// stops the start, like the other persisted state quality owns.
func (l *Loop) loadBenchmarks(ctx context.Context, now time.Time) error {
	l.benchmarksAt = now
	if l.db == nil {
		return nil
	}
	anchors, scores, err := l.readBenchmarks(ctx)
	if err != nil {
		return fmt.Errorf("quality: read the benchmark scores: %w", err)
	}
	l.monitor.adoptBenchmarks(anchors, scores)
	return nil
}

// refreshBenchmarks rereads the store every benchmarkEvery. A failed read keeps the previous values and is logged:
// stored scores are evidence for routing, and a transient database error must not stop the node.
func (l *Loop) refreshBenchmarks(ctx context.Context, now time.Time) {
	if l.db == nil || now.Sub(l.benchmarksAt) < benchmarkEvery {
		return
	}
	l.benchmarksAt = now
	anchors, scores, err := l.readBenchmarks(ctx)
	if err != nil {
		l.logger.Error("quality: reload the benchmark scores; the previous values stay in force", "err", err)
		return
	}
	l.monitor.adoptBenchmarks(anchors, scores)
}

// readBenchmarks reads the current set's baselines and scores as one consistent state. A row this binary cannot use —
// another format, an unknown question, a fraction outside 0..1 — is dropped and logged: it can only come from a broken
// writer, and withholding one model's score is safer than scoring against it.
func (l *Loop) readBenchmarks(ctx context.Context) (map[uuid.UUID]benchmarkAnchor, map[suppliers.EndpointID]benchmarkState, error) {
	set := fixedBenchmark()
	anchors := make(map[uuid.UUID]benchmarkAnchor)
	scores := make(map[suppliers.EndpointID]benchmarkState)
	err := l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) error {
		if err := l.readBaselines(ctx, tx, set, anchors); err != nil {
			return err
		}
		return l.readScores(ctx, tx, set, scores)
	})
	return anchors, scores, err
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
		var at, baseline int64
		if err := rows.Scan(&id, &model, &upstream, &formats, &score, &confidence, &at, &baseline); err != nil {
			return err
		}
		if !benchmarkScoreUsable(id, model, formats, score, confidence, at, baseline) {
			l.logger.Warn("quality: ignoring an unusable stored benchmark score", "endpoint", id.String())
			continue
		}
		out[id] = benchmarkState{
			version: set.Version, hash: set.Hash,
			baseline: time.Unix(0, baseline).UTC(), score: score, confidence: confidence,
			at:     time.Unix(0, at).UTC(),
			config: EndpointConfig{Model: model, Upstream: upstream, Formats: benchmarkFormatsOf(formats)},
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
func benchmarkScoreUsable(id, model uuid.UUID, formats int32, score, confidence float64, at, baseline int64) bool {
	return id != (uuid.UUID{}) && model != (uuid.UUID{}) && formats >= 0 && formats <= 0x3f &&
		at > 0 && baseline > 0 && score >= 0 && score <= 100 && confidence >= 0 && confidence <= 1
}
