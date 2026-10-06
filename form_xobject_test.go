package pdf

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestNestedFormXObjectTextExtraction(t *testing.T) {
	data := nestedFormXObjectPDF()
	reader, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	plain, err := reader.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if got := strings.TrimSpace(plain); got != "Nested text" {
		t.Fatalf("plain text = %q, want %q", got, "Nested text")
	}

	var blocks []TextBlock
	err = reader.WalkPages(func(number int, page Page) error {
		if number != 1 {
			t.Fatalf("page number = %d, want 1", number)
		}
		return page.WalkTextBlocks(func(block TextBlock) error {
			blocks = append(blocks, block)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("WalkPages/WalkTextBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Text != "Nested" || blocks[1].Text != "text" {
		t.Fatalf("block texts = %q, want [Nested text]", []string{blocks[0].Text, blocks[1].Text})
	}
	if math.Abs(blocks[0].BaselineX-37) > 0.001 || math.Abs(blocks[0].BaselineY-52) > 0.001 {
		t.Fatalf(
			"baseline = (%.3f, %.3f), want (37, 52)",
			blocks[0].BaselineX,
			blocks[0].BaselineY,
		)
	}
}

func TestLayoutTextBlocksAddsLineBreaksAndWordSpaces(t *testing.T) {
	blocks := []TextBlock{
		{Text: "ПЛАТЁЖНЫЙ ДОКУМЕНТ за ЯНВАРЬ 2000", X: 10, Width: 210, Height: 10, BaselineY: 818, FontSize: 10},
		{Text: "для внесения платы", X: 10, Width: 90, Height: 10, BaselineY: 808, FontSize: 10},
		{Text: "за содержание", X: 106, Width: 70, Height: 10, BaselineY: 808, FontSize: 10},
	}
	want := "ПЛАТЁЖНЫЙ ДОКУМЕНТ за ЯНВАРЬ 2000\nдля внесения платы за содержание"
	if got := layoutTextBlocks(blocks, 0); got != want {
		t.Fatalf("layoutTextBlocks() = %q, want %q", got, want)
	}
}

func TestLayoutTextBlocksSortsSuperscriptWithinItsVisualLine(t *testing.T) {
	blocks := []TextBlock{
		{Text: "Собственник:", X: 12, Width: 57, Height: 11.4, BaselineY: 789, FontSize: 8},
		{Text: "№ л/с: 1111111111", X: 320, Width: 75, Height: 11.4, BaselineY: 789, FontSize: 8},
		{Text: "ЕЛС: 11АА111111", X: 420, Width: 78, Height: 11.4, BaselineY: 789, FontSize: 8},
		{Text: "<1>", X: 438, Width: 9, Height: 7.1, BaselineY: 791.76, FontSize: 5},
	}
	want := "Собственник: № л/с: 1111111111 ЕЛС: 11АА111111<1>"
	if got := layoutTextBlocks(blocks, 0); got != want {
		t.Fatalf("layoutTextBlocks() = %q, want %q", got, want)
	}
}

func TestTextBlockCoalescerPreservesTrailingSpace(t *testing.T) {
	var blocks []TextBlock
	coalescer := textBlockCoalescer{executor: func(block TextBlock) error {
		blocks = append(blocks, block)
		return nil
	}}
	for _, block := range []TextBlock{
		{Text: "h", X: 0, Width: 1, BaselineY: 10, Font: "F", FontSize: 10},
		{Text: "ttps", X: 1, Width: 4, BaselineY: 10, Font: "F", FontSize: 10, trailingSpace: true},
		{Text: "или", X: 5, Width: 3, BaselineY: 10, Font: "F", FontSize: 10},
	} {
		if err := coalescer.add(block); err != nil {
			t.Fatal(err)
		}
	}
	if err := coalescer.flush(); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].Text != "https" || !blocks[0].trailingSpace || blocks[1].Text != "или" {
		t.Fatalf("blocks = %#v", blocks)
	}
}

func nestedFormXObjectPDF() []byte {
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, 9)
	writeObject := func(number int, body string) {
		offsets[number] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeStream := func(number int, dictionary, content string) {
		writeObject(number, fmt.Sprintf(
			"<< %s /Length %d >>\nstream\n%s\nendstream",
			dictionary,
			len(content)+1,
			content,
		))
	}

	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /XObject << /Outer 4 0 R >> >> /Contents 5 0 R >>")
	writeStream(4, "/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Matrix [2 0 0 2 10 20] /Resources << /XObject << /Inner 6 0 R >> >>", "/Inner Do")
	writeStream(5, "", "q 1 0 0 1 3 4 cm /Outer Do Q")
	writeStream(6, "/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Matrix [1 0 0 1 5 6] /Resources << /Font << /F1 7 0 R >> >>", "BT /F1 10 Tf 7 8 Td (Nested text) Tj ET")
	writeObject(7, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	writeObject(8, "<< >>")

	xrefOffset := output.Len()
	output.WriteString("xref\n0 9\n0000000000 65535 f \n")
	for number := 1; number <= 8; number++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size 9 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return output.Bytes()
}
