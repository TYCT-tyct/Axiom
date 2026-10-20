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

// Rotation manages version metadata and stored envelopes, using the platform scope assembled by cmd/.
// Nodes must be stopped for activation and retirement: an already loaded Keyring is immutable.
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

// ensureNoWriters refuses a lifecycle change while any node's heartbeat is recent. A serve that was missed keeps writing
// envelopes under the version the change is about to move away from, and those rows would then be unreadable. The window
// is kv's heartbeat window, so a stopped node stops counting after the same missed beats.
func (r Rotation) ensureNoWriters(ctx context.Context, tx pg.Tx) error {
	var alive bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM platform.nodes WHERE seen_at > now() - make_interval(secs => $1))`,
		kv.AliveFor.Seconds()).Scan(&alive)
	if err != nil {
		return fmt.Errorf("keyring: cannot read node heartbeats: %w", err)
	}
	if alive {
		return fmt.Errorf("keyring: a node's heartbeat is within %s; stop every serve instance and wait for its row to expire",
			kv.AliveFor)
	}
	return nil
}

// Check reads the ring at Path, then bootstraps legacy metadata once, checks every required version, and returns an
// immutable ring whose write version comes from the database. Call it before starting any worker or accepting requests.
func (r Rotation) Check(ctx context.Context) (*Keyring, KeyStatus, error) {
	return r.check(ctx, nil)
}

// CheckRing is Check on a ring the caller has already read. A node reads the file before it opens the database, so a
// file it cannot use is reported without one, and the transaction still reads the file only once.
func (r Rotation) CheckRing(ctx context.Context, ring *Keyring) (*Keyring, KeyStatus, error) {
	return r.check(ctx, ring)
}

func (r Rotation) check(ctx context.Context, ring *Keyring) (*Keyring, KeyStatus, error) {
	var status KeyStatus
	err := r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		var err error
		if ring == nil {
			ring, err = Load(r.Path)
			if err != nil {
				return err
			}
		}
		if err := bootstrapRing(ctx, tx, ring); err != nil {
			return err
		}
		status, err = checkVersions(ctx, tx, ring, 0)
		if err != nil {
			return err
		}
		if err := r.checkReferences(ctx, tx, ring, status); err != nil {
			return err
		}
		ring.latest = status.Active
		return nil
	})
	if err != nil {
		return nil, KeyStatus{}, err
	}
	return ring, status, nil
}

// bootstrapRing seeds the metadata from the file the first time a database-backed node starts, so a ring that predates
// the table keeps writing under the version its file says is the write version.
func bootstrapRing(ctx context.Context, tx pg.Tx, ring *Keyring) error {
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
func (c *versionCheck) add(ring *Keyring, version uint32, fingerprint []byte, state string, retiring uint32) {
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
		return fmt.Errorf("keyring: retired versions still in the local file: %v", c.held)
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

func checkVersions(ctx context.Context, tx pg.Tx, ring *Keyring, retiring uint32) (KeyStatus, error) {
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
		if state != "" {
			return nil
		}
		if err := addKey(r.Path, version); err != nil {
			return err
		}
		ring, err := Load(r.Path)
		if err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, version, ring.fingerprints[version], "readable"); err != nil {
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

// Activate chooses a distributed readable version for subsequent node starts and rewrap batches.
func (r Rotation) Activate(ctx context.Context, version uint32) error {
	return r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		if err := r.ensureNoWriters(ctx, tx); err != nil {
			return err
		}
		ring, err := Load(r.Path)
		if err != nil {
			return err
		}
		status, err := checkVersions(ctx, tx, ring, 0)
		if err != nil {
			return err
		}
		if err := r.checkReferences(ctx, tx, ring, status); err != nil {
			return err
		}
		if status.Active == version {
			return nil
		}
		if !slices.Contains(status.Readable, version) {
			return fmt.Errorf("keyring: version %d is not readable", version)
		}
		if _, err := tx.Exec(ctx, "UPDATE platform.key_versions SET state = 'readable' WHERE state = 'active'"); err != nil {
			return fmt.Errorf("keyring: cannot deactivate write version: %w", err)
		}
		if _, err := tx.Exec(ctx, "UPDATE platform.key_versions SET state = 'active' WHERE version = $1", int64(version)); err != nil {
			return fmt.Errorf("keyring: cannot activate write version: %w", err)
		}
		return rotationAudit(ctx, tx, "activate", version, map[string]any{"previous": status.Active})
	})
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
	if err := r.ensureNoWriters(ctx, tx); err != nil {
		return err
	}
	ring, err := Load(r.Path)
	if err != nil {
		return err
	}
	if _, err := checkVersions(ctx, tx, ring, version); err != nil {
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
