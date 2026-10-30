package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"axiom/internal/platform/config"
	"axiom/internal/platform/objectstore"
)

const objectstoreUsage = "usage: axiom objectstore create [--allow-existing] | check | list [--prefix P] [--names] | put --key K --file F [--content-type T] [--timeout D] | get --key K --file F [--timeout D] | delete (--key K | --prefix P)"

// The command's deadlines: a check or one metadata call may wait out a slow bucket; a transfer may take as long as a
// large archive needs, bounded so a stuck upload cannot live until the process is killed.
const (
	objectstoreMetaTimeout     = 30 * time.Second
	objectstoreMoveTimeout     = 30 * time.Minute
	objectstoreTransferTimeout = 30 * time.Minute
)

// transferContext bounds one transfer. It is separate so the deadline can be tested with a controlled clock, without
// making the operation wait on a real network or a wall clock.
func transferContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

// objectPutter and objectGetter are the narrow parts of objectstore.Client a transfer uses, so a test can substitute a
// controlled transfer and observe the context handed to it.
type objectPutter interface {
	Put(context.Context, objectstore.Upload) (objectstore.ObjectInfo, error)
}

type objectGetter interface {
	Get(context.Context, string) (*objectstore.Object, error)
}

// objectstoreCommand runs the operator's object-store verbs against AXIOM_OBJECTSTORE_*. It is how a deployment checks
// a new bucket, creates one, and moves a backup copy or an archive in and out; the backup job calls the same verbs with
// its own bucket, and the program's own archive features will call the same client.
func objectstoreCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(objectstoreUsage)
	}
	verb := args[0]
	switch verb {
	case "create", "check", "list", "put", "get", "delete":
	default:
		return fmt.Errorf("unknown verb %q\n%s", verb, objectstoreUsage)
	}
	env := config.New(environ)
	cfg, err := objectstore.ReadConfig(env)
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	if err := env.Problems(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	client, err := objectstore.Open(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	return runObjectstoreVerb(ctx, client, cfg.Bucket, verb, args[1:], out)
}

// runObjectstoreVerb parses one verb's flags and runs it; the client and the configuration are already checked.
//
//nolint:cyclop // one branch per verb: this is the command's whole surface
func runObjectstoreVerb(ctx context.Context, client *objectstore.Client, bucket, verb string, args []string, out io.Writer) error {
	var key, file, prefix, contentType string
	var names, allowExisting bool
	timeout := objectstoreTransferTimeout
	err := flags("objectstore "+verb, objectstoreUsage, args, func(set *flag.FlagSet) {
		switch verb {
		case "create":
			set.BoolVar(&allowExisting, "allow-existing", false, "succeed when the bucket already exists")
		case "put":
			set.StringVar(&key, "key", "", "the object key to write")
			set.StringVar(&file, "file", "", "the file to upload")
			set.StringVar(&contentType, "content-type", "", "the object's content type; default application/octet-stream")
			set.DurationVar(&timeout, "timeout", objectstoreTransferTimeout, "how long the whole transfer may take; e.g. 12h")
		case "get":
			set.StringVar(&key, "key", "", "the object key")
			set.StringVar(&file, "file", "", "where to write the object")
			set.DurationVar(&timeout, "timeout", objectstoreTransferTimeout, "how long the whole transfer may take; e.g. 12h")
		case "delete":
			set.StringVar(&key, "key", "", "the object key")
			set.StringVar(&prefix, "prefix", "", "every object under this prefix")
		case "list":
			set.StringVar(&prefix, "prefix", "", "only keys under this prefix")
			set.BoolVar(&names, "names", false, "print keys alone, one per line, for a shell")
		}
	})
	if err != nil {
		return err
	}
	switch verb {
	case "create":
		ctx, cancel := context.WithTimeout(ctx, objectstoreMetaTimeout)
		defer cancel()
		created, err := client.Create(ctx, allowExisting)
		if err != nil {
			return err
		}
		if !created && !allowExisting {
			return fmt.Errorf("objectstore: bucket %q already exists; run check, or create --allow-existing if you own it", bucket)
		}
		return writeLine(out, map[string]any{"kind": "objectstore", "bucket": bucket, "created": created})
	case "check":
		ctx, cancel := context.WithTimeout(ctx, objectstoreMetaTimeout)
		defer cancel()
		if err := client.Check(ctx); err != nil {
			return err
		}
		return writeLine(out, map[string]any{"kind": "objectstore", "bucket": bucket, "checked": true})
	case "list":
		ctx, cancel := context.WithTimeout(ctx, objectstoreMoveTimeout)
		defer cancel()
		return listObjects(ctx, client, prefix, names, out)
	case "put":
		if timeout <= 0 {
			return errors.New("objectstore put: --timeout must be positive")
		}
		return putObject(ctx, client, key, file, contentType, timeout, out)
	case "get":
		if timeout <= 0 {
			return errors.New("objectstore get: --timeout must be positive")
		}
		return getObject(ctx, client, key, file, timeout, out)
	default:
		switch {
		case key != "" && prefix != "":
			return errors.New("objectstore delete: give --key or --prefix, not both\n" + objectstoreUsage)
		case prefix != "":
			ctx, cancel := context.WithTimeout(ctx, objectstoreMoveTimeout)
			defer cancel()
			deleted, err := deletePrefix(ctx, client, prefix)
			if err != nil {
				return err
			}
			return writeLine(out, map[string]any{"kind": "objectstore", "prefix": prefix, "deleted": deleted})
		case key != "":
			ctx, cancel := context.WithTimeout(ctx, objectstoreMetaTimeout)
			defer cancel()
			if err := client.Delete(ctx, key); err != nil {
				return err
			}
			return writeLine(out, map[string]any{"kind": "objectstore", "key": key, "deleted": true})
		default:
			return errors.New("objectstore delete: --key or --prefix is required\n" + objectstoreUsage)
		}
	}
}

// deletePrefix removes every object under a prefix, one key at a time. It collects the keys first: deleting while
// following a continuation token makes a server's next page depend on objects this call has already removed.
func deletePrefix(ctx context.Context, client *objectstore.Client, prefix string) (int, error) {
	var keys []string
	token := ""
	for {
		page, err := client.List(ctx, objectstore.ListOptions{Prefix: prefix, ContinuationToken: token, MaxKeys: 1000})
		if err != nil {
			return 0, err
		}
		for _, object := range page.Objects {
			keys = append(keys, object.Key)
		}
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	for _, key := range keys {
		if err := client.Delete(ctx, key); err != nil {
			return 0, fmt.Errorf("objectstore delete %q: %w", key, err)
		}
	}
	return len(keys), nil
}

// listObjects follows every page, because the caller asked for the keys under a prefix, not for one page of 1000.
func listObjects(ctx context.Context, client *objectstore.Client, prefix string, names bool, out io.Writer) error {
	token := ""
	for {
		page, err := client.List(ctx, objectstore.ListOptions{Prefix: prefix, ContinuationToken: token, MaxKeys: 1000})
		if err != nil {
			return err
		}
		for _, object := range page.Objects {
			if names {
				if _, err := fmt.Fprintln(out, object.Key); err != nil {
					return err
				}
				continue
			}
			if err := writeLine(out, map[string]any{
				"kind": "objectstore", "key": object.Key, "size": object.Size,
				"modified": object.LastModified, "etag": object.ETag,
			}); err != nil {
				return err
			}
		}
		if page.NextToken == "" {
			return nil
		}
		token = page.NextToken
	}
}

func putObject(ctx context.Context, client objectPutter, key, path, contentType string, timeout time.Duration, out io.Writer) error {
	if key == "" || path == "" {
		return errors.New("objectstore put: --key and --file are required\n" + objectstoreUsage)
	}
	file, err := os.Open(path) //nolint:gosec // the file is the operator's own argument to an operator command
	if err != nil {
		return fmt.Errorf("objectstore put: %w", err)
	}
	defer file.Close() //nolint:errcheck // reading a file needs no close result
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("objectstore put: %w", err)
	}
	ctx, cancel := transferContext(ctx, timeout)
	defer cancel()
	stored, err := client.Put(ctx, objectstore.Upload{Key: key, Body: file, Size: info.Size(), ContentType: contentType})
	if err != nil {
		return err
	}
	return writeLine(out, map[string]any{"kind": "objectstore", "key": stored.Key, "size": stored.Size, "etag": stored.ETag})
}

// getObject writes through a temporary file in the destination's directory and renames it when complete, so a failed
// download leaves no partial object at the path a caller was told to use.
func getObject(ctx context.Context, client objectGetter, key, path string, timeout time.Duration, out io.Writer) error {
	if key == "" || path == "" {
		return errors.New("objectstore get: --key and --file are required\n" + objectstoreUsage)
	}
	ctx, cancel := transferContext(ctx, timeout)
	defer cancel()
	object, err := client.Get(ctx, key)
	if err != nil {
		return err
	}
	defer object.Body.Close() //nolint:errcheck // a completed read closed with no error; a failed one is reported below
	temp, err := os.CreateTemp(filepath.Dir(path), ".objectstore-*")
	if err != nil {
		return fmt.Errorf("objectstore get: %w", err)
	}
	name := temp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(name) //nolint:errcheck // a temporary file that could not be removed is reported by the next run
		}
	}()
	size, copyErr := io.Copy(temp, object.Body)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("objectstore get %q: %w", key, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("objectstore get %q: %w", key, err)
	}
	keep = true
	return writeLine(out, map[string]any{"kind": "objectstore", "key": object.Key, "file": path, "size": size})
}
