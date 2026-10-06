// Copyright 2026 Sergey Zonov.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestWalkTexts(t *testing.T) {
	reader := newTextTestReader(t)
	var texts []string
	err := reader.Page(1).WalkTexts(func(text string) error {
		texts = append(texts, text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"one", "two", "three", "four", "five"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q, want %q", texts, want)
	}
}

func TestWalkTextsStopsEarly(t *testing.T) {
	reader := newTextTestReader(t)
	var texts []string
	err := reader.Page(1).WalkTexts(func(text string) error {
		texts = append(texts, text)
		if len(texts) == 2 {
			return ErrStopTexts
		}
		return nil
	})
	if !errors.Is(err, ErrStopTexts) {
		t.Fatalf("WalkTexts error = %v, want ErrStopTexts", err)
	}
	want := []string{"one", "two"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q, want %q", texts, want)
	}
}

func TestWalkTextsRejectsNilExecutor(t *testing.T) {
	reader := newTextTestReader(t)
	if err := reader.Page(1).WalkTexts(nil); err == nil {
		t.Fatal("WalkTexts(nil) returned nil error")
	}
}

func TestWalkTextsReadsNestedFormXObject(t *testing.T) {
	data := nestedFormXObjectPDF()
	reader, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	if err := reader.Page(1).WalkTexts(func(text string) error {
		texts = append(texts, text)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Nested text"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q, want %q", texts, want)
	}
}

func newTextTestReader(t *testing.T) *Reader {
	t.Helper()
	data := textTestPDF()
	reader, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func textTestPDF() []byte {
	var output bytes.Buffer
	offsets := make([]int, 6)
	output.WriteString("%PDF-1.4\n")
	writeObject := func(number int, body string) {
		offsets[number] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	writeObject(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	content := `BT /F1 10 Tf (one) Tj [(two) 20 (three)] TJ (four) ' 0 0 (five) " ET`
	writeObject(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content))

	xrefOffset := output.Len()
	output.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for number := 1; number <= 5; number++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return output.Bytes()
}
