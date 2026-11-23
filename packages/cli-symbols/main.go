package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "upload" {
		fmt.Fprintln(os.Stderr, "Usage: caliper-symbols upload --server URL --app-id ID --platform android|ios --version VER --build BUILD --kind r8_mapping|dsym|hermes_sourcemap|breakpad|native_elf --file PATH [--git-sha SHA] [--env prod] [--api-key KEY]")
		os.Exit(2)
	}

	fsFlag := flag.NewFlagSet("upload", flag.ExitOnError)
	serverURL := fsFlag.String("server", "http://127.0.0.1:8080", "Caliper server base URL")
	appID := fsFlag.String("app-id", "", "Application identifier")
	platform := fsFlag.String("platform", "", "android or ios")
	env := fsFlag.String("env", "prod", "Environment (prod|staging|dev)")
	version := fsFlag.String("version", "", "App version")
	build := fsFlag.String("build", "", "Build number")
	gitSHA := fsFlag.String("git-sha", "", "Git commit SHA")
	kind := fsFlag.String("kind", "", "Symbol kind: r8_mapping|dsym|hermes_sourcemap|breakpad|native_elf")
	filePath := fsFlag.String("file", "", "Path to symbol file or .dSYM bundle directory")
	apiKey := fsFlag.String("api-key", "", "Optional X-Caliper-Key")
	_ = fsFlag.Parse(os.Args[2:])

	if *appID == "" || (*platform != "android" && *platform != "ios") || *version == "" || *build == "" || *kind == "" || *filePath == "" {
		fmt.Fprintln(os.Stderr, "error: --app-id, --platform, --version, --build, --kind, and --file are required")
		os.Exit(2)
	}

	content, err := loadSymbolArtifact(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read symbol file: %v\n", err)
		os.Exit(1)
	}
	if len(content) == 0 {
		fmt.Fprintln(os.Stderr, "error: symbol file is empty")
		os.Exit(1)
	}

	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	symbolID := fmt.Sprintf("sym-%s-%s-%s-%s", *platform, *version, *build, digest[:8])
	base := strings.TrimRight(*serverURL, "/")
	client := &http.Client{Timeout: 30 * time.Second}

	// 1. Register release metadata atomically with exact build identity.
	relPayload, _ := json.Marshal(map[string]any{
		"app_id":       *appID,
		"platform":     *platform,
		"env":          *env,
		"app_version":  *version,
		"build_number": *build,
		"git_sha":      *gitSHA,
		"symbol_id":    symbolID,
	})
	reqRel, err := http.NewRequest(http.MethodPost, base+"/v1/releases/register", bytes.NewReader(relPayload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "build release request: %v\n", err)
		os.Exit(1)
	}
	reqRel.Header.Set("Content-Type", "application/json")
	if *apiKey != "" {
		reqRel.Header.Set("X-Caliper-Key", *apiKey)
	}
	respRel, err := client.Do(reqRel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "register release: %v\n", err)
		os.Exit(1)
	}
	relBody, _ := io.ReadAll(respRel.Body)
	respRel.Body.Close()
	if respRel.StatusCode < 200 || respRel.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "register release failed (HTTP %d): %s\n", respRel.StatusCode, string(relBody))
		os.Exit(1)
	}

	// 2. Upload gzip-compressed symbol artifact with SHA-256 verification.
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(content); err != nil {
		fmt.Fprintf(os.Stderr, "gzip symbol: %v\n", err)
		os.Exit(1)
	}
	if err := gz.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close gzip: %v\n", err)
		os.Exit(1)
	}
	gzipLen := gzBuf.Len()

	params := url.Values{
		"app_id":       {*appID},
		"platform":     {*platform},
		"env":          {*env},
		"app_version":  {*version},
		"build_number": {*build},
		"kind":         {*kind},
		"sha256":       {digest},
		"symbol_id":    {symbolID},
	}
	if *gitSHA != "" {
		params.Set("git_sha", *gitSHA)
	}
	reqSym, err := http.NewRequest(http.MethodPost, base+"/v1/symbols/upload?"+params.Encode(), &gzBuf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build upload request: %v\n", err)
		os.Exit(1)
	}
	reqSym.Header.Set("Content-Type", "application/octet-stream")
	reqSym.Header.Set("Content-Encoding", "gzip")
	if *apiKey != "" {
		reqSym.Header.Set("X-Caliper-Key", *apiKey)
	}
	respSym, err := client.Do(reqSym)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upload symbol: %v\n", err)
		os.Exit(1)
	}
	symBody, _ := io.ReadAll(respSym.Body)
	respSym.Body.Close()
	if respSym.StatusCode != http.StatusCreated {
		fmt.Fprintf(os.Stderr, "upload symbol failed (HTTP %d): %s\n", respSym.StatusCode, string(symBody))
		os.Exit(1)
	}

	fmt.Printf("Uploaded symbol successfully:\n  symbol_id: %s\n  sha256:    %s\n  raw_bytes: %d (gzip %d)\n  receipt:   %s\n",
		symbolID, digest, len(content), gzipLen, strings.TrimSpace(string(symBody)))
}

// loadSymbolArtifact reads a regular file or auto-zips a .dSYM bundle directory into memory.
func loadSymbolArtifact(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return os.ReadFile(path)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	rootName := filepath.Base(filepath.Clean(path))
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(filepath.Join(rootName, rel)))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
