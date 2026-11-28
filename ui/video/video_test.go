package video

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		if got, want := fileURL(`C:\Users\jq\a b.mp4`), "file:///C:/Users/jq/a%20b.mp4"; got != want {
			t.Fatalf("fileURL = %q, want %q", got, want)
		}
		if got, want := fileURL(`\\server\share\film.mp4`), "file:////server/share/film.mp4"; got != want {
			t.Fatalf("fileURL of a UNC path = %q, want %q", got, want)
		}
	}
	if got, want := fileURL("/tmp/a b/c.mp4"), "file:///tmp/a%20b/c.mp4"; got != want {
		t.Fatalf("fileURL = %q, want %q", got, want)
	}
	if got, want := fileURL("/tmp/100#.mp4"), "file:///tmp/100%23.mp4"; got != want {
		t.Fatalf("fileURL = %q, want %q", got, want)
	}
	abs, err := filepath.Abs("trailer.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if got := fileURL(abs); !strings.HasPrefix(got, "file:///") || !strings.HasSuffix(got, "/trailer.mp4") {
		t.Fatalf("fileURL(%q) = %q", abs, got)
	}
}

func TestOpenDataRejectsEmptyData(t *testing.T) {
	if _, err := OpenData(nil); err == nil {
		t.Fatal("OpenData(nil) opened a video")
	}
	if _, err := OpenData([]byte{}); err == nil {
		t.Fatal("OpenData of no bytes opened a video")
	}
}
