package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestWritePlainTextMatchesGetPlainText(t *testing.T) {
	reader := newPlainTextTestReader(t)
	var streamed bytes.Buffer
	if err := reader.WritePlainText(&streamed); err != nil {
		t.Fatal(err)
	}
	if got, want := streamed.String(), "one\nthree"; got != want {
		t.Fatalf("WritePlainText() = %q, want %q", got, want)
	}

	buffered, err := reader.GetPlainText()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(buffered)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != streamed.String() {
		t.Fatalf("GetPlainText() = %q, want %q", got, streamed.String())
	}
}

func TestWritePlainTextReturnsWriterError(t *testing.T) {
	want := errors.New("write failed")
	err := newPlainTextTestReader(t).WritePlainText(errorWriter{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("WritePlainText() error = %v, want %v", err, want)
	}
}

func TestWritePlainTextRejectsShortWrite(t *testing.T) {
	err := newPlainTextTestReader(t).WritePlainText(shortWriter{})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WritePlainText() error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestWritePlainTextRejectsNilWriter(t *testing.T) {
	if err := newPlainTextTestReader(t).WritePlainText(nil); err == nil {
		t.Fatal("WritePlainText(nil) returned nil error")
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func newPlainTextTestReader(t *testing.T) *Reader {
	t.Helper()
	data := plainTextTestPDF()
	reader, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func plainTextTestPDF() []byte {
	var output bytes.Buffer
	offsets := make([]int, 9)
	writeObject := func(number int, body string) {
		offsets[number] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeStream := func(number int, content string) {
		writeObject(number, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content))
	}

	output.WriteString("%PDF-1.4\n")
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>")
	resources := "/Resources << /Font << /F1 8 0 R >> >>"
	writeObject(3, "<< /Type /Page /Parent 2 0 R "+resources+" /Contents 6 0 R >>")
	writeObject(4, "<< /Type /Page /Parent 2 0 R "+resources+" >>")
	writeObject(5, "<< /Type /Page /Parent 2 0 R "+resources+" /Contents 7 0 R >>")
	writeStream(6, "BT /F1 10 Tf (one) Tj ET")
	writeStream(7, "BT /F1 10 Tf (three) Tj ET")
	writeObject(8, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefOffset := output.Len()
	output.WriteString("xref\n0 9\n0000000000 65535 f \n")
	for number := 1; number <= 8; number++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size 9 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return output.Bytes()
}
