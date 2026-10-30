package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
)

// Rotation manages version metadata and stored envelopes, using the platform scope assembled by cmd/. Activation and
// retirement wait on the key reports every live node writes to platform.nodes; a running node installs a reloaded
// snapshot, so neither requires stopping the processes that seal envelopes.
type Rotation struct {
	DB     *pg.DB
	Scope  pg.Scope
	Path   string
	Stores []EnvelopeStore
}

// KeyStatus contains only version identifiers, never key material or fingerprints.
type KeyStatus struct {
	Active   uint32   `json:"active"`
	Readable []uint32 `json:"readable"`
	Retired  []uint32 `json:"retired"`
}

// transaction serializes lifecycle changes and each rewrap batch, and bounds lock waits and database work.
func (r Rotation) transaction(ctx context.Context, fn func(context.Context, pg.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return r.DB.Tx(ctx, r.Scope, func(tx pg.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(321, 8)"); err != nil {
			return fmt.Errorf("keyring: cannot acquire rotation lock: %w", err)
		}
		return fn(ctx, tx)
	})
}

// ErrNotDistributed reports that a live node has not reported the version a lifecycle step needs yet. It is the normal
// answer while a distribution spreads, not a failure: a background caller waits for the next heartbeat.
var ErrNotDistributed = errors.New("keyring: a live node has not reported the version yet")

// ErrQuiet reports that a live node has not written the active version long enough to rule out a seal started under the
// version being retired. It too is a wait, not a failure.
var ErrQuiet = errors.New("keyring: a live node has not written the active version long enough")

// ErrReferenced reports that envelopes still point at a version, so retirement has to wait for rewrap.
var ErrReferenced = errors.New("keyring: still referenced")

// errHeldRetired reports a tombstoned version that is still in the local distribution file. Retirement commits before
// it prunes, so a process that stopped in between leaves this state; Check and Refresh clear it themselves, which is also
// how a node that was offline during the retirement recovers on its next start.
var errHeldRetired = errors.New("keyring: retired versions still in the local file")

// ensureReaders refuses activation until every live node's heartbeat lists the version: a running node that cannot read a
// version must not have new envelopes sealed under it. The window is kv's heartbeat window, so a stopped node stops
// counting after the same missed beats, and the file, not the database, is what distributes the material.
func ensureReaders(ctx context.Context, tx pg.Tx, version uint32) error {
	var missing int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.nodes
		WHERE seen_at > now() - make_interval(secs => $1) AND NOT ($2 = ANY(key_versions))`,
		kv.AliveFor.Seconds(), int64(version)).Scan(&missing)
	if err != nil {
		return fmt.Errorf("keyring: cannot read node key reports: %w", err)
	}
	if missing != 0 {
		return fmt.Errorf("%w: %d live node(s) do not report version %d", ErrNotDistributed, missing, version)
	}
	return nil
}

// ensureQuiet refuses retirement until every live node has written the active version for kv's heartbeat window. A
// process that loaded the old version can still commit a seal it started before noticing the switch; waiting one window
// keeps that ordinary case from failing. The database fence, not this window, is what makes a late write safe: an
// envelope write takes the fence lock shared and a retirement takes it exclusively, so a write either commits before
// the tombstone (and the reference check sees it) or waits and is refused by the marker.
func ensureQuiet(ctx context.Context, tx pg.Tx, active uint32) error {
	var unready int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.nodes
		WHERE seen_at > now() - make_interval(secs => $1)
		  AND (key_active <> $2 OR NOT ($2 = ANY(key_versions)) OR key_active_since > now() - make_interval(secs => $3))`,
		kv.AliveFor.Seconds(), int64(active), kv.AliveFor.Seconds()).Scan(&unready)
	if err != nil {
		return fmt.Errorf("keyring: cannot read node key reports: %w", err)
	}
	if unready != 0 {
		return fmt.Errorf("%w: %d live node(s) are not on version %d", ErrQuiet, unready, active)
	}
	return nil
}

// Check reads the ring at Path, then bootstraps legacy metadata once, checks every required version, and returns an
// immutable ring whose write version comes from the database. Call it before starting any worker or accepting requests.
func (r Rotation) Check(ctx context.Context) (*Keyring, KeyStatus, error) {
	return r.check(ctx, nil)
}

// CheckRing is Check on a ring the caller has already read. A node reads the file before it opens the database, so a
// file it cannot use is reported without one, and the transaction reads the file itself only when a held retired
// version has to be pruned.
func (r Rotation) CheckRing(ctx context.Context, ring *Keyring) (*Keyring, KeyStatus, error) {
	return r.check(ctx, ring)
}

func (r Rotation) check(ctx context.Context, ring *Keyring) (*Keyring, KeyStatus, error) {
	var status KeyStatus
	err := r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		var snapshot *keySnapshot
		if ring == nil {
			var err error
			if snapshot, err = loadSnapshot(r.Path); err != nil {
				return err
			}
			ring = &Keyring{}
		} else {
			snapshot = ring.snapshot()
		}
		if err := bootstrapRing(ctx, tx, snapshot); err != nil {
			return err
		}
		var err error
		if snapshot, status, err = r.reconcile(ctx, tx, snapshot); err != nil {
			return err
		}
		if err := r.checkReferences(ctx, tx, snapshot, status); err != nil {
			return err
		}
		ring.install(snapshot.withActive(status.Active).withConfirmed(status.Readable))
		return nil
	})
	if err != nil {
		return nil, KeyStatus{}, err
	}
	return ring, status, nil
}

// reconcile checks the versions the database knows against one file read. A retired version that is still in the file is
// the state a retirement leaves when its process stops between the commit and the file edit; when no envelope references
// it, the file is pruned and read again so the caller installs the ring the database describes. A referenced version is
// never deleted: that state means the tombstone and the stored envelopes disagree, and the key may still be the only way
// to read them.
func (r Rotation) reconcile(ctx context.Context, tx pg.Tx, snapshot *keySnapshot) (*keySnapshot, KeyStatus, error) {
	status, err := checkVersions(ctx, tx, snapshot, 0)
	if !errors.Is(err, errHeldRetired) {
		return snapshot, status, err
	}
	if err := r.pruneRetired(ctx, tx); err != nil {
		return nil, KeyStatus{}, err
	}
	if snapshot, err = loadSnapshot(r.Path); err != nil {
		return nil, KeyStatus{}, err
	}
	status, err = checkVersions(ctx, tx, snapshot, 0)
	return snapshot, status, err
}

// Refresh reads the distribution file again and installs the whole ring with the database's write version. A node calls
// it on a heartbeat-sized tick: a version written by an operator then reaches every live node without a restart, and a
// failed read leaves the previous ring serving rather than publishing a partial one. It checks the version metadata, not
// the envelope inventory: scanning every envelope store belongs to startup, activation and the commands, not to a tick
// that runs on every node for the life of the process. The one exception is a held retired version: pruning it first
// confirms that no envelope references it, because deleting the only key that reads a stored value cannot be undone.
func (r Rotation) Refresh(ctx context.Context, ring *Keyring) (KeyStatus, error) {
	var status KeyStatus
	err := r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		snapshot, err := loadSnapshot(r.Path)
		if err != nil {
			return err
		}
		if err := bootstrapRing(ctx, tx, snapshot); err != nil {
			return err
		}
		if snapshot, status, err = r.reconcile(ctx, tx, snapshot); err != nil {
			return err
		}
		ring.install(snapshot.withActive(status.Active).withConfirmed(status.Readable))
		return nil
	})
	if err != nil {
		return KeyStatus{}, err
	}
	return status, nil
}

// bootstrapRing seeds the metadata from the file the first time a database-backed node starts, so a ring that predates
// the table keeps writing under the version its file says is the write version.
func bootstrapRing(ctx context.Context, tx pg.Tx, ring *keySnapshot) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT FROM platform.key_versions)").Scan(&exists); err != nil {
		return fmt.Errorf("keyring: cannot read version metadata: %w", err)
	}
	if exists {
		return nil
	}
	for version, fingerprint := range ring.fingerprints {
		state := "readable"
		if version == ring.latest {
			state = "active"
		}
		if err := insertVersion(ctx, tx, version, fingerprint, state); err != nil {
			return err
		}
	}
	return rotationAudit(ctx, tx, "initialize", ring.latest, map[string]any{"versions": len(ring.keys)})
}

func insertVersion(ctx context.Context, tx pg.Tx, version uint32, fingerprint [32]byte, state string) error {
	if _, err := tx.Exec(ctx, "INSERT INTO platform.key_versions (version, fingerprint, state) VALUES ($1, $2, $3)",
		int64(version), fingerprint[:], state); err != nil {
		// A unique violation may include the digest in PostgreSQL's detail.
		return fmt.Errorf("keyring: cannot register version %d", version)
	}
	return nil
}

// versionCheck collects what the database says about each version and how the local ring compares.
type versionCheck struct {
	readable, retired, missing, mismatched, held []uint32
	active                                       uint32
}

func newVersionCheck() versionCheck {
	return versionCheck{readable: []uint32{}, retired: []uint32{}}
}

// add records one row. A retired version that is still in the local file is held, unless it is the version being
// retired: its tombstone is already committed and the same command prunes the file.
func (c *versionCheck) add(ring *keySnapshot, version uint32, fingerprint []byte, state string, retiring uint32) {
	if state == "retired" {
		c.retired = append(c.retired, version)
		if _, ok := ring.fingerprints[version]; ok && version != retiring {
			c.held = append(c.held, version)
		}
		return
	}
	c.readable = append(c.readable, version)
	if state == "active" {
		c.active = version
	}
	if local, ok := ring.fingerprints[version]; !ok {
		c.missing = append(c.missing, version)
	} else if !bytes.Equal(local[:], fingerprint) {
		c.mismatched = append(c.mismatched, version)
	}
}

// err is nil only when every version the database knows is held locally with the same material, no retired version
// remains in the file, and one version is active.
func (c *versionCheck) err() error {
	if len(c.held) > 0 {
		return fmt.Errorf("%w: %v", errHeldRetired, c.held)
	}
	if len(c.missing)+len(c.mismatched) > 0 {
		return fmt.Errorf("keyring: missing versions %v; mismatched versions %v", c.missing, c.mismatched)
	}
	if c.active == 0 {
		return errors.New("keyring: no active version")
	}
	return nil
}

func (c *versionCheck) status() KeyStatus {
	return KeyStatus{Active: c.active, Readable: c.readable, Retired: c.retired}
}

func checkVersions(ctx context.Context, tx pg.Tx, ring *keySnapshot, retiring uint32) (KeyStatus, error) {
	rows, err := tx.Query(ctx, "SELECT version, fingerprint, state FROM platform.key_versions ORDER BY version")
	if err != nil {
		return KeyStatus{}, fmt.Errorf("keyring: cannot read versions: %w", err)
	}
	defer rows.Close()
	found := newVersionCheck()
	for rows.Next() {
		var version uint32
		var fingerprint []byte
		var state string
		if err := rows.Scan(&version, &fingerprint, &state); err != nil {
			return KeyStatus{}, fmt.Errorf("keyring: cannot decode versions: %w", err)
		}
		found.add(ring, version, fingerprint, state, retiring)
	}
	if err := rows.Err(); err != nil {
		return KeyStatus{}, fmt.Errorf("keyring: cannot read versions: %w", err)
	}
	if err := found.err(); err != nil {
		return KeyStatus{}, err
	}
	return found.status(), nil
}

// Add generates and durably stores a new readable key without changing the current write version.
func (r Rotation) Add(ctx context.Context, version uint32) error {
	if version == 0 {
		return errors.New("keyring: version must be positive")
	}
	// Commit the legacy write version before touching the file. A crash must not make the new maximum active.
	if _, _, err := r.Check(ctx); err != nil {
		return err
	}
	return r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		state, err := versionState(ctx, tx, version)
		if err != nil {
			return err
		}
		if state == "retired" {
			return fmt.Errorf("keyring: version %d is retired", version)
		}
		if err := addKey(r.Path, version); err != nil {
			return err
		}
		if state != "" {
			return nil
		}
		snapshot, err := loadSnapshot(r.Path)
		if err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, version, snapshot.fingerprints[version], "readable"); err != nil {
			return err
		}
		return rotationAudit(ctx, tx, "add", version, map[string]any{})
	})
}

func versionState(ctx context.Context, tx pg.Tx, version uint32) (string, error) {
	var state string
	err := tx.QueryRow(ctx, "SELECT state FROM platform.key_versions WHERE version = $1", int64(version)).Scan(&state)
	if errors.Is(err, pg.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("keyring: cannot read version state: %w", err)
	}
	return state, nil
}

// Activate chooses a distributed readable version for subsequent node starts and rewrap batches. Every live node must
// report the version in its heartbeat first: activation changes what new envelopes are sealed under, so a node that
// cannot read it must not let the switch happen.
func (r Rotation) Activate(ctx context.Context, version uint32) error {
	return r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		snapshot, err := loadSnapshot(r.Path)
		if err != nil {
			return err
		}
		status, err := checkVersions(ctx, tx, snapshot, 0)
		if err != nil {
			return err
		}
		if status.Active == version {
			return nil
		}
		if !slices.Contains(status.Readable, version) {
			return fmt.Errorf("keyring: version %d is not readable", version)
		}
		// The cheap wait comes before the inventory scan: a lifecycle retries this every heartbeat while a distribution
		// spreads, and only the attempt that finds every node ready has to read every envelope store.
		if err := ensureReaders(ctx, tx, version); err != nil {
			return err
		}
		if err := r.checkReferences(ctx, tx, snapshot, status); err != nil {
			return err
		}
		if err := setActive(ctx, tx, version); err != nil {
			return err
		}
		return rotationAudit(ctx, tx, "activate", version, map[string]any{"previous": status.Active})
	})
}

// setActive moves the single write version to version; the partial unique index allows one active row.
func setActive(ctx context.Context, tx pg.Tx, version uint32) error {
	if _, err := tx.Exec(ctx, "UPDATE platform.key_versions SET state = 'readable' WHERE state = 'active'"); err != nil {
		return fmt.Errorf("keyring: cannot deactivate write version: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE platform.key_versions SET state = 'active' WHERE version = $1", int64(version)); err != nil {
		return fmt.Errorf("keyring: cannot activate write version: %w", err)
	}
	return nil
}

// pruneRetired removes tombstoned versions still present in the distribution file. It recovers the state a retirement
// leaves when the process stops between its commit and its file edit. A version is deleted only after confirming that no
// envelope references it: the tombstone is written by a retirement that checked the same thing, so a reference here
// means the metadata and the stored data disagree and the key may still be the only way to read it.
func (r Rotation) pruneRetired(ctx context.Context, tx pg.Tx) error {
	rows, err := tx.Query(ctx, "SELECT version FROM platform.key_versions WHERE state = 'retired' ORDER BY version")
	if err != nil {
		return fmt.Errorf("keyring: cannot read retired versions: %w", err)
	}
	defer rows.Close()
	var retired []uint32
	for rows.Next() {
		var version uint32
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("keyring: cannot decode retired version: %w", err)
		}
		retired = append(retired, version)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("keyring: cannot read retired versions: %w", err)
	}
	for _, version := range retired {
		if err := r.unreferenced(ctx, tx, version); err != nil {
			return fmt.Errorf("keyring: refusing to prune version %d: %w", version, err)
		}
		// A retired version always has a marker: the tombstone and the marker commit together, and migration
		// 00020 backfilled the rows from before the fence. A missing marker means this state was edited by hand; deleting
		// the key could then race a writer the fence never saw, so leave it in place.
		var fenced bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT FROM platform.key_retirements WHERE version = $1)",
			int64(version)).Scan(&fenced); err != nil {
			return fmt.Errorf("keyring: cannot read retirement fence: %w", err)
		}
		if !fenced {
			return fmt.Errorf("keyring: refusing to prune version %d: no retirement fence", version)
		}
		if err := removeKey(r.Path, version); err != nil {
			return err
		}
	}
	return nil
}

// Retire refuses an active or referenced version. The tombstone commits before the local file is pruned.
func (r Rotation) Retire(ctx context.Context, version uint32) error {
	if err := r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		return r.tombstone(ctx, tx, version)
	}); err != nil {
		return err
	}
	// File edits share the lifecycle lock too: pruning outside it could overwrite a concurrent add's file.
	return r.transaction(ctx, func(_ context.Context, _ pg.Tx) error {
		return removeKey(r.Path, version)
	})
}

// tombstone records the retirement, or finds it already recorded by a run that stopped before pruning its file.
func (r Rotation) tombstone(ctx context.Context, tx pg.Tx, version uint32) error {
	// Every envelope write takes this lock shared in its row trigger and holds it to commit. Holding it exclusively
	// makes the reference check, the marker and the tombstone one decision with respect to those writers: a writer that
	// acquired the lock first commits before the check sees the row; one that arrives later is refused by the marker.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(321, 9)"); err != nil {
		return fmt.Errorf("keyring: cannot acquire retirement fence: %w", err)
	}
	snapshot, err := loadSnapshot(r.Path)
	if err != nil {
		return err
	}
	status, err := checkVersions(ctx, tx, snapshot, version)
	if err != nil {
		return err
	}
	// The write version has to be this old one's successor on every live node for long enough that a seal started under
	// the old version is usually gone before retirement; a remaining one is caught by the fence below.
	if err := ensureQuiet(ctx, tx, status.Active); err != nil {
		return err
	}
	state, err := versionState(ctx, tx, version)
	if err != nil {
		return err
	}
	if state == "" || state == "active" {
		return fmt.Errorf("keyring: version %d is unknown or active", version)
	}
	if err := r.unreferenced(ctx, tx, version); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO platform.key_retirements (version) VALUES ($1) ON CONFLICT DO NOTHING",
		int64(version)); err != nil {
		return fmt.Errorf("keyring: cannot fence retired version: %w", err)
	}
	if state == "retired" {
		return nil
	}
	if _, err := tx.Exec(ctx, "UPDATE platform.key_versions SET state = 'retired' WHERE version = $1", int64(version)); err != nil {
		return fmt.Errorf("keyring: cannot retire version: %w", err)
	}
	return rotationAudit(ctx, tx, "retire", version, map[string]any{})
}

func rotationAudit(ctx context.Context, tx pg.Tx, action string, version uint32, detail map[string]any) error {
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: audit.System("platform.keyring"), Action: "keyring." + action,
		Target: audit.Target{Module: "platform", Kind: "key_version", ID: strconv.FormatUint(uint64(version), 10)},
		Detail: detail,
	}); err != nil {
		return fmt.Errorf("keyring: cannot record audit: %w", err)
	}
	return nil
}
