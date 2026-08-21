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

## Walking positioned text

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

The original APIs for styled text, rows, columns, and page content remain
available. Runnable examples are in [`examples`](examples).

## License

This project is distributed under the BSD 3-Clause License. See
[`LICENSE`](LICENSE).
