package eval

import (
	"context"
	"errors"
	"strconv"
)

// Fetching is the one effect this package will not perform on its own.
//
// Everything else the configure phase does reads or writes inside directories
// the caller named. A download reaches outside all of them, to a host the
// project chose, and runs on whatever the other end returns. That is a decision
// for the program embedding this one, not for the language evaluator, so it
// arrives as an interface on the State: a caller that supplies none gets a
// clear refusal rather than a silent network request.

// ErrNoDownloader is reported when a project asks to fetch something and the
// caller supplied no Downloader.
var ErrNoDownloader = errors.New("this configuration does not perform downloads")

// Downloader retrieves a URL.
type Downloader interface {
	// Download writes the contents of url to dest and reports what happened.
	// A non-nil error means the transfer did not complete; an HTTP status that
	// is not success is reported that way rather than as an error, because
	// file(DOWNLOAD) hands both to the project through STATUS.
	Download(ctx context.Context, req DownloadRequest) (DownloadResult, error)
}

// DownloadRequest is one transfer.
type DownloadRequest struct {
	URL  string
	Dest string // empty to return the body rather than write a file

	// ExpectedHash is "<algo>=<value>", the form file(DOWNLOAD) takes. A
	// mismatch fails the transfer and removes the file, because a partially
	// trusted download left on disk is worse than none.
	ExpectedHash string

	Headers   []string
	TLSVerify bool
	Timeout   int // seconds; zero means the implementation's default
}

// DownloadResult is what came back.
type DownloadResult struct {
	Code    int    // 0 on success, non-zero for a transfer that failed
	Message string // "No error" on success, else what went wrong
	Body    string // the content, when Dest was empty
	Log     string
}

// fileDownload implements file(DOWNLOAD).
func fileDownload(ctx context.Context, e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("file DOWNLOAD called with incorrect number of arguments")
	}
	req := DownloadRequest{URL: v[1], TLSVerify: true}
	statusVar, logVar := "", ""

	i := 2
	// The destination is positional when it is there at all, and absent when
	// the caller only wants the status.
	if i < len(v) && !downloadKeyword(v[i]) {
		req.Dest = e.state.absPath(v[i])
		i++
	}
	for ; i < len(v); i++ {
		switch v[i] {
		case "STATUS":
			statusVar = next(v, i)
			i++
		case "LOG":
			logVar = next(v, i)
			i++
		case "EXPECTED_HASH":
			req.ExpectedHash = next(v, i)
			i++
		case "EXPECTED_MD5":
			req.ExpectedHash = "MD5=" + next(v, i)
			i++
		case "TIMEOUT", "INACTIVITY_TIMEOUT":
			if n, err := strconv.Atoi(next(v, i)); err == nil {
				req.Timeout = n
			}
			i++
		case "HTTPHEADER":
			req.Headers = append(req.Headers, next(v, i))
			i++
		case "TLS_VERIFY":
			req.TLSVerify = IsOn(next(v, i))
			i++
		case "SHOW_PROGRESS", "NETRC", "NETRC_FILE", "TLS_CAINFO", "USERPWD",
			"RANGE_START", "RANGE_END", "TLS_VERSION":
			// Accepted; either they change only reporting, or they select a
			// transport feature this implementation does not vary.
			if !downloadFlagOnly(v[i]) {
				i++
			}
		}
	}

	if e.state.Downloader == nil {
		// A refusal the project can see, rather than a silent empty file.
		if statusVar != "" {
			e.state.SetVar(statusVar, JoinList([]string{"1", ErrNoDownloader.Error()}))
			return nil
		}
		return e.fatalf("file DOWNLOAD: %v", ErrNoDownloader)
	}

	if req.Dest != "" {
		if err := e.fs.MkdirAll(dirOf(req.Dest)); err != nil {
			return e.fatalf("file DOWNLOAD could not create %s: %v", dirOf(req.Dest), err)
		}
	}
	res, err := e.state.Downloader.Download(ctx, req)
	if err != nil {
		res.Code, res.Message = 1, err.Error()
	}
	if logVar != "" {
		e.state.SetVar(logVar, res.Log)
	}
	if statusVar != "" {
		e.state.SetVar(statusVar, JoinList([]string{strconv.Itoa(res.Code), res.Message}))
		return nil
	}
	// Without a STATUS variable there is nowhere to report a failure, so it
	// becomes an error: a project that did not ask for the status is saying it
	// expects the download to work.
	if res.Code != 0 {
		return e.fatalf("file DOWNLOAD failed:\n  %s\n  %s", req.URL, res.Message)
	}
	return nil
}

// downloadKeyword reports whether an argument starts an option rather than
// being the destination file.
func downloadKeyword(s string) bool {
	switch s {
	case "STATUS", "LOG", "EXPECTED_HASH", "EXPECTED_MD5", "TIMEOUT",
		"INACTIVITY_TIMEOUT", "HTTPHEADER", "TLS_VERIFY", "SHOW_PROGRESS",
		"NETRC", "NETRC_FILE", "TLS_CAINFO", "USERPWD", "RANGE_START",
		"RANGE_END", "TLS_VERSION":
		return true
	}
	return false
}

// downloadFlagOnly reports whether an option takes no value.
func downloadFlagOnly(s string) bool { return s == "SHOW_PROGRESS" }

// fileUpload refuses.
//
// Downloading is a project fetching what it declared it needs. Uploading sends
// this machine's files somewhere the project named, which is a different act
// with a different blast radius, and no build has ever needed it to produce a
// binary.
func fileUpload(e *evaluator, v []string) error {
	return e.fatalf("file UPLOAD is not implemented: sending files to a remote host is not " +
		"something building a project requires")
}
