package cmake_test

import (
	"context"
	"strings"
	"testing"

	cmake "github.com/vertex-language/go-cmake"
	"github.com/vertex-language/go-cmake/run"
)

// configureWithoutDownloader runs the library with no Downloader supplied,
// which is the default a program embedding this package gets until it decides
// otherwise.
func configureWithoutDownloader(t *testing.T, source string) string {
	t.Helper()
	var out strings.Builder
	c, err := cmake.New(cmake.Config{
		Source: source,
		Binary: source + "/b",
		FS:     cmake.RealFS(""),
		Runner: run.OS(),
		Out:    &out,
		Err:    &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Configure(context.Background()); err != nil {
		out.WriteString(err.Error())
	}
	return out.String()
}
