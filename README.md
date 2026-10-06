# PDF Reader

`github.com/szonov/pdf` is a fork of
[`github.com/ledongthuc/pdf`](https://github.com/ledongthuc/pdf), which in turn
is based on [`rsc.io/pdf`](https://github.com/rsc/pdf).

The package reads PDF files and extracts plain or positioned text. This fork
also provides:

- O(N) sequential page-tree traversal without repeatedly looking pages up by
  number;
- early termination while walking pages or text blocks;
- incremental extraction of positioned text blocks;
- axis-aligned text bounds calculated from character codes, font metrics, text
  state, and the current transformation matrix.

## Installation

```sh
go get github.com/szonov/pdf
```

The module requires Go 1.24.1 or newer.

## Reading plain text

Use `Reader.WritePlainText` to process the document sequentially without
retaining all extracted text in memory:

```go
if err := reader.WritePlainText(os.Stdout); err != nil {
	log.Fatal(err)
}
```

`Reader.GetPlainText` remains available when an `io.Reader` is required. It
uses the same one-pass page traversal internally but buffers the complete
result before returning.

```go
package main

import (
	"fmt"
	"io"
	"log"

	"github.com/szonov/pdf"
)

func main() {
	file, reader, err := pdf.Open("document.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	text, err := reader.GetPlainText()
	if err != nil {
		log.Fatal(err)
	}

	contents, err := io.ReadAll(text)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(contents))
}
```

## Walking pages

`Reader.WalkPages` visits pages in document order. Page numbers start at 1.
Returning an error from the callback stops the traversal and returns that
error. Use `pdf.ErrStopWalking` when early termination is expected.

```go
err := reader.WalkPages(func(number int, page pdf.Page) error {
	fmt.Printf("page %d\n", number)
	if number == 10 {
		return pdf.ErrStopWalking
	}
	return nil
})
if err != nil && !errors.Is(err, pdf.ErrStopWalking) {
	log.Fatal(err)
}
```

Unlike repeatedly calling `Reader.Page`, this method traverses the PDF page
tree once and can stop before later pages are resolved.

## Walking text

`Page.WalkTexts` emits decoded strings from PDF text-showing operators in
content-stream order. It does not calculate coordinates or font metrics and
does not merge adjacent strings, so it is suitable for fast prefix searches
and other cases where layout is not needed.

```go
texts := 0
err := page.WalkTexts(func(text string) error {
	fmt.Print(text)
	texts++
	if texts == 10 {
		return pdf.ErrStopTexts
	}
	return nil
})
if err != nil && !errors.Is(err, pdf.ErrStopTexts) {
	log.Fatal(err)
}
```

For a `TJ` array, the callback is invoked separately for each string element;
numeric positioning adjustments are skipped. Text inside Form XObjects is
visited recursively. Return `pdf.ErrStopTexts` to stop immediately without
interpreting the rest of the page.

## Walking positioned text blocks

`Page.WalkTextBlocks` interprets a page's content streams in order and emits
positioned blocks incrementally. Adjacent fragments with the same font and
baseline may be combined into one block.

Coordinates and dimensions are expressed in page-space points. `X` and `Y`
identify the lower-left corner of the axis-aligned bounds. `BaselineX` and
`BaselineY` identify the transformed text origin after text rise is applied.

```go
err := page.WalkTextBlocks(func(block pdf.TextBlock) error {
	fmt.Printf(
		"%q font=%s size=%.1f bounds=(%.1f, %.1f, %.1f, %.1f)\n",
		block.Text,
		block.Font,
		block.FontSize,
		block.X,
		block.Y,
		block.Width,
		block.Height,
	)
	return nil
})
if err != nil && !errors.Is(err, pdf.ErrStopTextBlocks) {
	log.Fatal(err)
}
```

Return `pdf.ErrStopTextBlocks` from the callback to stop interpreting the rest
of the page. Other callback and parsing errors are returned unchanged.

Shared resource dictionaries, decoded font encodings, glyph widths, and font
geometry are cached by the reader. This avoids recalculating the same font data
for every page while keeping page content streams out of the cache.

The original APIs for styled text, rows, columns, and page content remain
available. Runnable examples are in [`examples`](examples).

## License

This project is distributed under the BSD 3-Clause License. See
[`LICENSE`](LICENSE).
