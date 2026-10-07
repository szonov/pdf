package pdf

import (
	"bytes"
	"strings"
	"testing"
)

func TestWritePagesCopiesPageWithoutReencodingStreams(t *testing.T) {
	source := nestedFormXObjectPDF()
	reader, err := NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := reader.WritePages(&output, []int{1}); err != nil {
		t.Fatal(err)
	}

	result, err := NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if result.NumPage() != 1 {
		t.Fatalf("NumPage() = %d, want 1", result.NumPage())
	}
	text, err := result.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(text); got != "Nested text" {
		t.Fatalf("plain text = %q, want %q", got, "Nested text")
	}
}

func TestWritePagesRejectsInvalidSelection(t *testing.T) {
	source := nestedFormXObjectPDF()
	reader, err := NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}

	for _, pages := range [][]int{nil, {0}, {2}} {
		var output bytes.Buffer
		if err := reader.WritePages(&output, pages); err == nil {
			t.Fatalf("WritePages(%v) succeeded", pages)
		}
	}
}

func TestWritePagesPreservesOrderAndDeduplicatesResources(t *testing.T) {
	source := plainTextTestPDF()
	reader, err := NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := reader.WritePages(&output, []int{3, 1}); err != nil {
		t.Fatal(err)
	}
	result, err := NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if result.NumPage() != 2 {
		t.Fatalf("NumPage() = %d, want 2", result.NumPage())
	}
	for pageNumber, want := range []string{"three", "one"} {
		text, err := result.Page(pageNumber + 1).GetPlainText(nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(text); got != want {
			t.Fatalf("page %d text = %q, want %q", pageNumber+1, got, want)
		}
	}
	firstFont := result.Page(1).Resources().Key("Font").Key("F1")
	secondFont := result.Page(2).Resources().Key("Font").Key("F1")
	if firstFont.ptr != secondFont.ptr {
		t.Fatalf("shared font copied twice: page 1 = %v, page 2 = %v", firstFont.ptr, secondFont.ptr)
	}
}
