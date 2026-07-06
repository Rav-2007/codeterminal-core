package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// bgeModelBaseURL is the pinned source for the embedding model files: the
// ONNX export of BAAI/bge-small-en-v1.5 maintained at Xenova/bge-small-en-v1.5
// (the standard conversion used by transformers.js; the upstream
// BAAI/bge-small-en-v1.5 repo only ships an unquantized ONNX export).
const bgeModelBaseURL = "https://huggingface.co/Xenova/bge-small-en-v1.5/resolve/main"

// modelAsset pins one file this build depends on: its expected size and
// sha256 make the download auditable and let EnsureModelFiles both skip the
// network on a cache hit and fail loudly on any corruption or tampering.
type modelAsset struct {
	name      string // filename under the model cache dir
	url       string // full source URL
	size      int64  // expected size in bytes
	sha256Hex string // expected sha256, lowercase hex
}

// bgeModelAssets are the exact files required to run BAAI/bge-small-en-v1.5
// (int8-quantized ONNX) plus its tokenizer. Every size and checksum here was
// verified by downloading the file and hashing it directly — update all
// three fields together if the pinned version ever changes.
var bgeModelAssets = []modelAsset{
	{
		name:      "model_int8.onnx",
		url:       bgeModelBaseURL + "/onnx/model_int8.onnx",
		size:      33760831,
		sha256Hex: "bf64d05457cb391fa88d045faf5927a15ea36d96228ddf23ea970087afdc1197",
	},
	{
		name:      "tokenizer.json",
		url:       bgeModelBaseURL + "/tokenizer.json",
		size:      711396,
		sha256Hex: "d241a60d5e8f04cc1b2b3e9ef7a4921b27bf526d9f6050ab90f9267a1f9e5c66",
	},
	{
		name:      "tokenizer_config.json",
		url:       bgeModelBaseURL + "/tokenizer_config.json",
		size:      366,
		sha256Hex: "9261e7d79b44c8195c1cada2b453e55b00aeb81e907a6664974b4d7776172ab3",
	},
	{
		name:      "special_tokens_map.json",
		url:       bgeModelBaseURL + "/special_tokens_map.json",
		size:      125,
		sha256Hex: "b6d346be366a7d1d48332dbc9fdf3bf8960b5d879522b7799ddba59e76237ee3",
	},
	{
		name:      "vocab.txt",
		url:       bgeModelBaseURL + "/vocab.txt",
		size:      231508,
		sha256Hex: "07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3",
	},
}

// progressLogEvery bounds how often a large download logs its progress.
const progressLogEvery = 4 << 20 // 4MB

// defaultModelCacheDir returns the per-user directory the real (non-test)
// call site caches BGE model files under.
func defaultModelCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".codeterminal", "models", "bge-small-en-v1.5-int8"), nil
}

// EnsureModelFiles makes sure every asset in assets is present under
// cacheDir with the expected size and checksum, downloading and verifying
// any that are missing or invalid. If every asset already matches, it makes
// no network requests at all. It never leaves a corrupt or mismatched file
// behind: a failed verification after download removes the bad file and
// returns an error rather than silently accepting it.
func EnsureModelFiles(ctx context.Context, cacheDir string, assets []modelAsset, logger *log.Logger) (string, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", fmt.Errorf("creating model cache dir %s: %w", cacheDir, err)
	}

	for _, asset := range assets {
		path := filepath.Join(cacheDir, asset.name)

		if err := verifyAsset(path, asset); err == nil {
			continue // cache hit: no network for this asset
		}

		logger.Printf("model cache: downloading %s (~%s)...", asset.name, humanSize(asset.size))
		if err := downloadAsset(ctx, path, asset, logger); err != nil {
			return "", fmt.Errorf("fetching %s: %w", asset.name, err)
		}

		if err := verifyAsset(path, asset); err != nil {
			os.Remove(path) // never leave a corrupt or mismatched file cached
			return "", fmt.Errorf("verifying %s after download: %w", asset.name, err)
		}
		logger.Printf("model cache: %s verified (size + sha256 match)", asset.name)
	}

	return cacheDir, nil
}

// verifyAsset reports a non-nil error if path doesn't exist, doesn't match
// asset's expected size, or doesn't match its expected sha256.
func verifyAsset(path string, asset modelAsset) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != asset.size {
		return fmt.Errorf("size mismatch: got %d bytes, want %d", info.Size(), asset.size)
	}
	sum, err := sha256File(path)
	if err != nil {
		return err
	}
	if sum != asset.sha256Hex {
		return fmt.Errorf("checksum mismatch: got %s, want %s", sum, asset.sha256Hex)
	}
	return nil
}

// downloadAsset fetches asset.url to destPath via a temp file + rename, so a
// download that's interrupted partway never leaves a partial file at the
// real destination path for verifyAsset to (mis)judge as present.
func downloadAsset(ctx context.Context, destPath string, asset modelAsset, logger *log.Logger) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", asset.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status fetching %s: %s", asset.url, resp.Status)
	}

	tmpPath := destPath + ".part"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmpPath, err)
	}

	progress := &progressLogger{logger: logger, name: asset.name, total: asset.size}
	_, copyErr := io.Copy(f, io.TeeReader(resp.Body, progress))
	closeErr := f.Close()

	if copyErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("writing %s: %w", tmpPath, copyErr)
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing %s: %w", tmpPath, closeErr)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, destPath, err)
	}
	return nil
}

// progressLogger implements io.Writer so it can sit in an io.TeeReader
// between the HTTP body and the destination file, logging download
// progress at most once every progressLogEvery bytes.
type progressLogger struct {
	logger    *log.Logger
	name      string
	total     int64
	written   int64
	lastLogAt int64
}

func (p *progressLogger) Write(b []byte) (int, error) {
	n := len(b)
	p.written += int64(n)
	if p.written-p.lastLogAt >= progressLogEvery || p.written == p.total {
		p.logger.Printf("model cache: %s: %s / %s", p.name, humanSize(p.written), humanSize(p.total))
		p.lastLogAt = p.written
	}
	return n, nil
}

func humanSize(n int64) string {
	const mb = 1 << 20
	return fmt.Sprintf("%.1fMB", float64(n)/mb)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runDownloadModelCommand implements `codeterminal-daemon download-model`:
// an explicit, manual trigger for EnsureModelFiles against the real pinned
// bgeModelAssets, plus the onnxruntime shared library the helper needs to
// run them. It is never invoked automatically.
func runDownloadModelCommand(args []string, logger *log.Logger) error {
	cacheDir, err := defaultModelCacheDir()
	if err != nil {
		return err
	}
	modelDir, err := EnsureModelFiles(context.Background(), cacheDir, bgeModelAssets, logger)
	if err != nil {
		return err
	}
	logger.Printf("model cache: ready at %s", modelDir)

	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		return err
	}
	ortLibPath, err := EnsureONNXRuntimeLib(context.Background(), ortCacheDir, logger)
	if err != nil {
		return err
	}
	logger.Printf("onnxruntime: ready at %s", ortLibPath)
	return nil
}
