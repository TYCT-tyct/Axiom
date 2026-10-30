package main

import (
	"fmt"

	"axiom/internal/platform/webui"
)

// webAssets is the command's argument handling: exactly one directory.
func webAssets(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: axiom web-assets <dir>")
	}
	return publishWebAssets(args[0])
}

// publishWebAssets writes this build's content-hashed frontend files into dir, keeping the files other releases put
// there. A deployment runs it from a release before any node of that release starts, so every node finds the files of
// both builds while old and new nodes overlap (internal/platform/README.md「前端文件」).
func publishWebAssets(dir string) error {
	frontend, err := webui.Embedded()
	if err != nil {
		return err
	}
	if err := frontend.Publish(dir); err != nil {
		return fmt.Errorf("%s: %w", webAssetsDirVar, err)
	}
	return nil
}
