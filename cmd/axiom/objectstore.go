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

const objectstoreUsage = "usage: axiom objectstore check | list [--prefix P] | put --key K --file F [--content-type T] | get --key K --file F | delete --key K"

// The command's deadlines: a check or one metadata call may wait out a slow bucket; a transfer may take as long as a
// large archive needs, bounded so a stuck upload cannot live until the process is killed.
const (
	objectstoreMetaTimeout = 30 * time.Second
	objectstoreMoveTimeout = 30 * time.Minute
)

// objectstoreCommand runs the operator's object-store verbs against AXIOM_OBJECTSTORE_*. It is how a deployment checks
// a new bucket and how an operator moves a backup copy or an archive in and out before a scheduled job exists; the
// program's own archive and content features will call the same client.
func objectstoreCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(objectstoreUsage)
	}
	verb := args[0]
	switch verb {
	case "check", "list", "put", "get", "delete":
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
	err := flags("objectstore "+verb, objectstoreUsage, args, func(set *flag.FlagSet) {
		switch verb {
		case "put":
			set.StringVar(&key, "key", "", "the object key to write")
			set.StringVar(&file, "file", "", "the file to upload")
			set.StringVar(&contentType, "content-type", "", "the object's content type; default application/octet-stream")
		case "get":
			set.StringVar(&key, "key", "", "the object key")
			set.StringVar(&file, "file", "", "where to write the object")
		case "delete":
			set.StringVar(&key, "key", "", "the object key")
		case "list":
			set.StringVar(&prefix, "prefix", "", "only keys under this prefix")
		}
	})
	if err != nil {
		return err
	}
	switch verb {
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
		return listObjects(ctx, client, prefix, out)
	case "put":
		return putObject(ctx, client, key, file, contentType, out)
	case "get":
		return getObject(ctx, client, key, file, out)
	default:
		ctx, cancel := context.WithTimeout(ctx, objectstoreMetaTimeout)
		defer cancel()
		if err := client.Delete(ctx, key); err != nil {
			return err
		}
		return writeLine(out, map[string]any{"kind": "objectstore", "key": key, "deleted": true})
	}
}

// listObjects follows every page, because the caller asked for the keys under a prefix, not for one page of 1000.
func listObjects(ctx context.Context, client *objectstore.Client, prefix string, out io.Writer) error {
	token := ""
	for {
		page, err := client.List(ctx, objectstore.ListOptions{Prefix: prefix, ContinuationToken: token, MaxKeys: 1000})
		if err != nil {
			return err
		}
		for _, object := range page.Objects {
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

func putObject(ctx context.Context, client *objectstore.Client, key, path, contentType string, out io.Writer) error {
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
	ctx, cancel := context.WithTimeout(ctx, objectstoreMoveTimeout)
	defer cancel()
	stored, err := client.Put(ctx, objectstore.Upload{Key: key, Body: file, Size: info.Size(), ContentType: contentType})
	if err != nil {
		return err
	}
	return writeLine(out, map[string]any{"kind": "objectstore", "key": stored.Key, "size": stored.Size, "etag": stored.ETag})
}

// getObject writes through a temporary file in the destination's directory and renames it when complete, so a failed
// download leaves no partial object at the path a caller was told to use.
func getObject(ctx context.Context, client *objectstore.Client, key, path string, out io.Writer) error {
	if key == "" || path == "" {
		return errors.New("objectstore get: --key and --file are required\n" + objectstoreUsage)
	}
	ctx, cancel := context.WithTimeout(ctx, objectstoreMoveTimeout)
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
