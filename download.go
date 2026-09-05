package cmake

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vertex-language/go-cmake/archive"
	"github.com/vertex-language/go-cmake/eval"
)

// httpDownloader is the Downloader that actually makes requests.
//
// It is a separate type from the evaluator's interface so that the decision to
// reach the network stays with whoever constructs it. A caller that wants a
// configure which cannot make requests supplies nothing and gets a refusal; one
// that wants requests logged, cached, or restricted to a mirror supplies its
// own.
type httpDownloader struct {
	client *http.Client
}

// defaultDownloadTimeout bounds a transfer that has stopped making progress.
// Without one a configure hangs on an unreachable host until someone kills it,
// which reads as the build tool being broken.
const defaultDownloadTimeout = 5 * time.Minute

func newHTTPDownloader() *httpDownloader {
	return &httpDownloader{client: &http.Client{Timeout: defaultDownloadTimeout}}
}

func (d *httpDownloader) Download(ctx context.Context, req eval.DownloadRequest) (eval.DownloadResult, error) {
	var log strings.Builder
	fmt.Fprintf(&log, "GET %s\n", req.URL)

	timeout := defaultDownloadTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return eval.DownloadResult{Code: 1, Message: err.Error(), Log: log.String()}, nil
	}
	for _, header := range req.Headers {
		if name, value, found := strings.Cut(header, ":"); found {
			httpReq.Header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return eval.DownloadResult{Code: 1, Message: err.Error(), Log: log.String()}, nil
	}
	defer resp.Body.Close()
	fmt.Fprintf(&log, "%s\n", resp.Status)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// An HTTP error is a status, not a transport failure: file(DOWNLOAD)
		// hands it to the project, which may well be probing for the file.
		return eval.DownloadResult{
			Code:    resp.StatusCode,
			Message: resp.Status,
			Log:     log.String(),
		}, nil
	}

	digest, err := newDigest(req.ExpectedHash)
	if err != nil {
		return eval.DownloadResult{Code: 1, Message: err.Error(), Log: log.String()}, nil
	}

	var body strings.Builder
	var out io.Writer = &body
	var file *os.File
	if req.Dest != "" {
		// The download goes to a temporary name and is renamed on success, so
		// that a failed or mismatched transfer never leaves a file that looks
		// complete at the path the project will read.
		file, err = os.CreateTemp(dirOfPath(req.Dest), ".download-*")
		if err != nil {
			return eval.DownloadResult{Code: 1, Message: err.Error(), Log: log.String()}, nil
		}
		defer os.Remove(file.Name())
		out = file
	}
	if digest != nil {
		out = io.MultiWriter(out, digest)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		if file != nil {
			file.Close()
		}
		return eval.DownloadResult{Code: 1, Message: err.Error(), Log: log.String()}, nil
	}
	if file != nil {
		if err := file.Close(); err != nil {
			return eval.DownloadResult{Code: 1, Message: err.Error(), Log: log.String()}, nil
		}
	}

	if digest != nil {
		got := hex.EncodeToString(digest.Sum(nil))
		want := expectedDigestValue(req.ExpectedHash)
		if !strings.EqualFold(got, want) {
			return eval.DownloadResult{
				Code: 1,
				Message: fmt.Sprintf("hash mismatch\n    expected: %s\n      actual: %s",
					want, got),
				Log: log.String(),
			}, nil
		}
	}

	if file != nil {
		if err := os.Rename(file.Name(), req.Dest); err != nil {
			return eval.DownloadResult{Code: 1, Message: err.Error(), Log: log.String()}, nil
		}
	}
	return eval.DownloadResult{
		Code:    0,
		Message: "No error",
		Body:    body.String(),
		Log:     log.String(),
	}, nil
}

// newDigest builds the hash an EXPECTED_HASH asks for, or nil when none was.
func newDigest(expected string) (hash.Hash, error) {
	if expected == "" {
		return nil, nil
	}
	algo, _, found := strings.Cut(expected, "=")
	if !found {
		return nil, fmt.Errorf("EXPECTED_HASH must be <algorithm>=<value>, got %q", expected)
	}
	switch strings.ToUpper(strings.TrimSpace(algo)) {
	case "MD5":
		return md5.New(), nil
	case "SHA1":
		return sha1.New(), nil
	case "SHA224":
		return sha256.New224(), nil
	case "SHA256":
		return sha256.New(), nil
	case "SHA384":
		return sha512.New384(), nil
	case "SHA512":
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unknown hash algorithm %q", algo)
	}
}

func expectedDigestValue(expected string) string {
	_, value, _ := strings.Cut(expected, "=")
	return strings.TrimSpace(value)
}

func dirOfPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}

// HTTPDownloader returns the Downloader that makes real requests.
//
// It is exported so that constructing one is a deliberate act by the calling
// program. The command line uses it because a person typing `cmake` expects a
// project's declared dependencies to be fetched; a library embedding this
// package decides for itself.
func HTTPDownloader() eval.Downloader { return newHTTPDownloader() }

// archiveExtractor unpacks what FetchContent downloaded.
//
// It is a type of its own rather than the archive package directly so that the
// evaluator keeps depending on an interface: unpacking an archive from the
// network is a decision, and a caller replacing it can inspect or refuse one.
type archiveExtractor struct{}

func (archiveExtractor) Extract(archivePath, dest string, stripComponents int) error {
	return archive.Extract(archivePath, dest, stripComponents)
}
