package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

// A PINNED FILE CAN COME FROM MORE THAN ONE PLACE.
//
// FOUND 2026-11-19: github.com/microsoft/onnxruntime answered 404 -- the
// repository, its releases, its API. Every new install failed at its first
// index ("fetching onnxruntime-linux-x64-1.26.0.tgz: 404 Not Found"), and CI's
// locate eval could not start. The archives are pinned by size and sha256, so
// WHERE the bytes come from was never what made them trustworthy: a copy of
// the same bytes is the same file, and a copy of different bytes is refused
// exactly as a corrupted download is. So a pinned asset may list mirrors,
// tried in order only when its own address fails.

// fetchVerified downloads asset to path from asset.url, then from each mirror
// in turn, and keeps the first file that matches the pinned size and sha256.
// A file that does not match is removed before the next source is tried. The
// error, when every source fails, says what each one did.
func fetchVerified(ctx context.Context, path string, asset modelAsset, logger *log.Logger) error {
	var failures []string
	for i, src := range append([]string{asset.url}, asset.mirrors...) {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err.Error())
			break
		}
		from := asset
		from.url = src
		if i > 0 {
			logger.Printf("model cache: %s: %s; trying the copy at %s", asset.name, failures[len(failures)-1], src)
		}
		if err := downloadAsset(ctx, path, from, logger); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := verifyAsset(path, asset); err != nil {
			os.Remove(path) // never leave a corrupt or mismatched file cached
			failures = append(failures, fmt.Sprintf("%s did not match its pin: %v", src, err))
			continue
		}
		if i > 0 {
			logger.Printf("model cache: %s came from the copy at %s and matches its pin", asset.name, src)
		}
		return nil
	}
	return errors.New(strings.Join(failures, "; then "))
}
