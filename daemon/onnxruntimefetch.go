package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// onnxRuntimeVersion is the pinned onnxruntime release. It must match the
// ORT_API_VERSION that github.com/yalue/onnxruntime_go (used by the helper)
// was built against — confirmed by comparing the ORT_API_VERSION constant
// in both projects' C API headers directly: both are 26, i.e. onnxruntime
// v1.26.0.
const onnxRuntimeVersion = "1.26.0"

// onnxRuntimePlatform pins one platform's onnxruntime release archive
// (verified by downloading and hashing it directly, same as bgeModelAssets)
// plus where the shared library lives once extracted.
type onnxRuntimePlatform struct {
	goos, goarch     string
	archive          modelAsset
	libPathInArchive string // path of the shared lib within the archive
	libFileName      string // final filename under the cache dir
}

// onnxRuntimePlatforms covers the three platforms verified so far. Intel
// Mac (darwin/amd64) is deliberately absent: upstream onnxruntime v1.26.0
// ships no prebuilt binary for it at all. This is a known, open gap — see
// "Known platform gaps" in README.md — not an oversight; lookupONNXRuntimePlatform
// returns a specific, clear error for it rather than a generic "unsupported
// platform" message.
var onnxRuntimePlatforms = []onnxRuntimePlatform{
	{
		goos:   "linux",
		goarch: "amd64",
		archive: modelAsset{
			name:      "onnxruntime-linux-x64-1.26.0.tgz",
			url:       "https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-linux-x64-1.26.0.tgz",
			size:      8590023,
			sha256Hex: "1254da24fb389cf39dc0ff3451ab48301740ffbfcbaf646849df92f80ee92c57",
		},
		libPathInArchive: "onnxruntime-linux-x64-1.26.0/lib/libonnxruntime.so.1.26.0",
		libFileName:      "libonnxruntime.so",
	},
	{
		goos:   "darwin",
		goarch: "arm64",
		archive: modelAsset{
			name:      "onnxruntime-osx-arm64-1.26.0.tgz",
			url:       "https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-osx-arm64-1.26.0.tgz",
			size:      31717869,
			sha256Hex: "7a1280bbb1701ea514f71828765237e7896e0f2e1cd332f1f70dbd5c3e33aca3",
		},
		libPathInArchive: "onnxruntime-osx-arm64-1.26.0/lib/libonnxruntime.1.26.0.dylib",
		libFileName:      "libonnxruntime.dylib",
	},
	{
		goos:   "windows",
		goarch: "amd64",
		archive: modelAsset{
			name:      "onnxruntime-win-x64-1.26.0.zip",
			url:       "https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-win-x64-1.26.0.zip",
			size:      75675381,
			sha256Hex: "6ebe99b5564bf4d029b6e93eac9ff423682b6212eade769e9ca3f685eaf500b4",
		},
		libPathInArchive: "onnxruntime-win-x64-1.26.0/lib/onnxruntime.dll",
		libFileName:      "onnxruntime.dll",
	},
}

// lookupONNXRuntimePlatform finds the pinned archive for goos/goarch. Intel
// Mac gets its own specific, loud error rather than falling into the
// generic "unsupported platform" message, since it's a deliberate,
// documented gap rather than an unconsidered one.
func lookupONNXRuntimePlatform(goos, goarch string) (onnxRuntimePlatform, error) {
	for _, p := range onnxRuntimePlatforms {
		if p.goos == goos && p.goarch == goarch {
			return p, nil
		}
	}
	if goos == "darwin" && goarch == "amd64" {
		return onnxRuntimePlatform{}, fmt.Errorf(
			"Intel Mac (darwin/amd64) is not supported: upstream onnxruntime v%s ships no prebuilt binary for this platform at all; this is a known, open platform-coverage gap (see \"Known platform gaps\" in README.md), not a bug",
			onnxRuntimeVersion)
	}
	return onnxRuntimePlatform{}, fmt.Errorf(
		"no pinned onnxruntime shared library for %s/%s; supported platforms: linux/amd64, darwin/arm64, windows/amd64",
		goos, goarch)
}

// defaultONNXRuntimeCacheDir returns the per-user directory the real
// (non-test) call site caches the extracted onnxruntime shared library
// under, separate from the BGE model cache since it's a different kind of
// asset (a native library, not a model+tokenizer).
func defaultONNXRuntimeCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".mochiii", "models", "onnxruntime-"+onnxRuntimeVersion), nil
}

// EnsureONNXRuntimeLib makes sure the onnxruntime shared library for the
// current platform is downloaded, verified, and extracted under cacheDir,
// returning its final path. Like EnsureModelFiles, a cache hit (the
// extracted library already present) makes no network requests at all.
func EnsureONNXRuntimeLib(ctx context.Context, cacheDir string, logger *log.Logger) (string, error) {
	platform, err := lookupONNXRuntimePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	libPath := filepath.Join(cacheDir, platform.libFileName)
	if info, err := os.Stat(libPath); err == nil && info.Size() > 0 {
		return libPath, nil // cache hit: no network at all
	}

	if _, err := EnsureModelFiles(ctx, cacheDir, []modelAsset{platform.archive}, logger); err != nil {
		return "", fmt.Errorf("fetching onnxruntime archive: %w", err)
	}

	archivePath := filepath.Join(cacheDir, platform.archive.name)
	logger.Printf("model cache: extracting %s from %s...", platform.libFileName, platform.archive.name)
	if err := extractArchiveMember(archivePath, platform.libPathInArchive, libPath); err != nil {
		return "", fmt.Errorf("extracting %s from %s: %w", platform.libFileName, archivePath, err)
	}
	logger.Printf("model cache: onnxruntime ready at %s", libPath)
	return libPath, nil
}

// onnxRuntimeLibCached reports whether the extracted shared library for THIS
// platform is already present, without downloading anything.
//
// It deliberately mirrors EnsureONNXRuntimeLib's cache-hit test above --
// os.Stat plus a non-zero size on the same libPath -- rather than inventing a
// second notion of "cached". The extracted library carries no pinned checksum
// of its own (the sha256 is on the ARCHIVE it came out of), so a stricter test
// here would claim a stronger guarantee than the download path itself provides.
//
// An unsupported platform reports false, not an error: on Intel Mac there is no
// library to have cached, and the honest answer to "is it there?" is no. The
// actionable "Intel Mac is not supported" message belongs to the download path,
// which is where a user can do something about it.
func onnxRuntimeLibCached(cacheDir string) bool {
	platform, err := lookupONNXRuntimePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(cacheDir, platform.libFileName))
	return err == nil && info.Size() > 0
}

// extractArchiveMember extracts exactly one named member from a .tgz or
// .zip archive to destPath, inferring the archive format from its file
// extension.
func extractArchiveMember(archivePath, memberPath, destPath string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tgz"), strings.HasSuffix(archivePath, ".tar.gz"):
		return extractFromTarGz(archivePath, memberPath, destPath)
	case strings.HasSuffix(archivePath, ".zip"):
		return extractFromZip(archivePath, memberPath, destPath)
	default:
		return fmt.Errorf("unrecognized archive format: %s", archivePath)
	}
}

func extractFromTarGz(archivePath, memberPath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("member %s not found in archive", memberPath)
		}
		if err != nil {
			return fmt.Errorf("reading tar entries: %w", err)
		}
		if hdr.Name != memberPath {
			continue
		}
		return writeExtractedFile(destPath, tr)
	}
}

func extractFromZip(archivePath, memberPath, destPath string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.Name != memberPath {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening zip member %s: %w", memberPath, err)
		}
		defer rc.Close()
		return writeExtractedFile(destPath, rc)
	}
	return fmt.Errorf("member %s not found in archive", memberPath)
}

// writeExtractedFile writes r to destPath via a temp file + rename, so an
// interrupted extraction never leaves a partial file at the real
// destination for a later cache-hit check to (mis)judge as present.
func writeExtractedFile(destPath string, r io.Reader) error {
	tmpPath := destPath + ".part"
	// O_NOFOLLOW: destPath comes from cache config, not client input, but refuse
	// to write the extracted library through a symlink planted at the .part name
	// rather than following it out of the cache dir. 0755 preserved — the
	// extracted artifact is a shared library that may need the exec bit.
	out, err := openNoFollow(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(out, r)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return closeErr
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// resolveModelPaths returns the cached BGE model directory and onnxruntime
// shared library path for the current platform, without downloading
// anything. It fails with a clear, actionable message if either is
// missing — the fix is always the same: run `download-model`.
func resolveModelPaths() (modelDir, onnxRuntimeLibPath string, err error) {
	modelDir, err = defaultModelCacheDir()
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(filepath.Join(modelDir, "model_int8.onnx")); statErr != nil {
		return "", "", fmt.Errorf("BGE model not found under %s; run `download-model` first", modelDir)
	}

	platform, err := lookupONNXRuntimePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", err
	}
	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		return "", "", err
	}
	onnxRuntimeLibPath = filepath.Join(ortCacheDir, platform.libFileName)
	if _, statErr := os.Stat(onnxRuntimeLibPath); statErr != nil {
		return "", "", fmt.Errorf("onnxruntime shared library not found at %s; run `download-model` first", onnxRuntimeLibPath)
	}

	return modelDir, onnxRuntimeLibPath, nil
}
