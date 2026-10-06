// Copyright 2026 Sergey Zonov.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrStopTextBlocks stops WalkTextBlocks without interpreting the rest of the page.
var ErrStopTextBlocks = errors.New("stop walking text blocks")

const maxFormXObjectDepth = 12

// TextBlock contains extracted text and its axis-aligned page-space bounds.
// Adjacent text-showing operations may be combined into one block.
// X and Y are the lower-left corner of the bounds. BaselineX and BaselineY are
// the page-space position of the text origin after applying text rise.
type TextBlock struct {
	Text string

	X      float64
	Y      float64
	Width  float64
	Height float64

	BaselineX float64
	BaselineY float64

	Font     string
	FontSize float64

	leadingSpace  bool
	trailingSpace bool
}

type positionedTextBlock struct {
	block      TextBlock
	line       float64
	start, end float64
}

type positionedTextLine struct {
	line, height float64
	blocks       []positionedTextBlock
}

// layoutTextBlocks reconstructs readable lines from the geometry already
// collected by WalkTextBlocks. It does not interpret the PDF a second time.
func layoutTextBlocks(blocks []TextBlock, rotation int64) string {
	positioned := make([]positionedTextBlock, 0, len(blocks))
	rotation = ((rotation % 360) + 360) % 360
	for _, block := range blocks {
		block.Text = strings.TrimSpace(block.Text)
		if block.Text == "" {
			continue
		}
		item := positionedTextBlock{block: block}
		switch rotation {
		case 90:
			item.line, item.start, item.end = block.BaselineX, -(block.Y + block.Height), -block.Y
		case 180:
			item.line, item.start, item.end = block.BaselineY, -(block.X + block.Width), -block.X
		case 270:
			item.line, item.start, item.end = -block.BaselineX, block.Y, block.Y+block.Height
		default:
			item.line, item.start, item.end = -block.BaselineY, block.X, block.X+block.Width
		}
		positioned = append(positioned, item)
	}
	sort.SliceStable(positioned, func(i, j int) bool {
		if positioned[i].line != positioned[j].line {
			return positioned[i].line < positioned[j].line
		}
		return positioned[i].start < positioned[j].start
	})

	lines := make([]positionedTextLine, 0, len(positioned))
	for _, item := range positioned {
		itemHeight := math.Max(math.Max(item.block.Height, item.block.FontSize), 1)
		if len(lines) == 0 {
			lines = append(lines, positionedTextLine{line: item.line, height: itemHeight, blocks: []positionedTextBlock{item}})
			continue
		}
		current := &lines[len(lines)-1]
		lineTolerance := math.Max(math.Max(current.height, itemHeight)*0.4, 1)
		if math.Abs(item.line-current.line) > lineTolerance {
			lines = append(lines, positionedTextLine{line: item.line, height: itemHeight, blocks: []positionedTextBlock{item}})
			continue
		}
		current.blocks = append(current.blocks, item)
		current.height = math.Max(current.height, itemHeight)
	}

	var result strings.Builder
	for lineIndex, textLine := range lines {
		sort.SliceStable(textLine.blocks, func(i, j int) bool {
			return textLine.blocks[i].start < textLine.blocks[j].start
		})
		if lineIndex > 0 {
			result.WriteByte('\n')
		}
		var end, height float64
		previousTrailingSpace := false
		for blockIndex, item := range textLine.blocks {
			itemHeight := math.Max(math.Max(item.block.Height, item.block.FontSize), 1)
			if blockIndex > 0 {
				gap := item.start - end
				if item.block.leadingSpace || previousTrailingSpace || gap > math.Max(math.Min(height, itemHeight)*0.15, 0.75) {
					result.WriteByte(' ')
				}
			}
			result.WriteString(item.block.Text)
			end = math.Max(end, item.end)
			height = math.Max(height, itemHeight)
			previousTrailingSpace = item.block.trailingSpace
		}
	}
	return result.String()
}

// WalkTextBlocks interprets positioned text on p and incrementally calls
// executor for extracted blocks. Adjacent fragments with the same font and
// baseline may be combined. Returning an error stops interpretation;
// ErrStopTextBlocks can be used for expected early termination.
func (p Page) WalkTextBlocks(executor func(TextBlock) error) (err error) {
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return nil
	}
	if executor == nil {
		return errors.New("nil text block executor")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("interpret text blocks: %v", recovered)
		}
	}()

	g := textBlockState{horizontalScale: 1, ctm: ident, textMatrix: ident, lineMatrix: ident}
	blocks := textBlockCoalescer{executor: executor}
	err = walkTextBlockContent(
		p.V.Key("Contents"),
		p.Resources(),
		&g,
		&blocks,
		0,
		make(map[objptr]bool),
	)
	if err != nil {
		return err
	}
	return blocks.flush()
}

func walkTextBlockContent(
	contents Value,
	resources Value,
	g *textBlockState,
	blocks *textBlockCoalescer,
	depth int,
	activeForms map[objptr]bool,
) error {
	var graphicsStack []textBlockState
	fonts := make(map[string]textBlockFont)

	return interpretTextUntil(contents, func(stk *Stack, op string) error {
		args := popTextArguments(stk)

		switch op {
		case "q":
			graphicsStack = append(graphicsStack, *g)
		case "Q":
			if len(graphicsStack) == 0 {
				return errors.New("unmatched Q operator")
			}
			*g = graphicsStack[len(graphicsStack)-1]
			graphicsStack = graphicsStack[:len(graphicsStack)-1]
		case "cm":
			m, err := textBlockMatrix(args, "cm")
			if err != nil {
				return err
			}
			g.ctm = m.mul(g.ctm)
		case "BT":
			g.textMatrix = ident
			g.lineMatrix = ident
		case "ET":
		case "Tf":
			if len(args) != 2 {
				return fmt.Errorf("Tf: want 2 operands, got %d", len(args))
			}
			fontName := args[0].Name()
			cached, ok := fonts[fontName]
			if !ok {
				font := Font{V: resources.Key("Font").Key(fontName)}
				cached = textBlockFont{font: font, encoding: font.Encoder(), widths: make(map[string]float64)}
				fonts[fontName] = cached
			}
			g.font = cached.font
			g.encoding = cached.encoding
			g.widths = cached.widths
			g.fontSize = args[1].Float64()
		case "Tc":
			if len(args) != 1 {
				return fmt.Errorf("Tc: want 1 operand, got %d", len(args))
			}
			g.charSpacing = args[0].Float64()
		case "Tw":
			if len(args) != 1 {
				return fmt.Errorf("Tw: want 1 operand, got %d", len(args))
			}
			g.wordSpacing = args[0].Float64()
		case "Tz":
			if len(args) != 1 {
				return fmt.Errorf("Tz: want 1 operand, got %d", len(args))
			}
			g.horizontalScale = args[0].Float64() / 100
		case "Ts":
			if len(args) != 1 {
				return fmt.Errorf("Ts: want 1 operand, got %d", len(args))
			}
			g.rise = args[0].Float64()
		case "TL":
			if len(args) != 1 {
				return fmt.Errorf("TL: want 1 operand, got %d", len(args))
			}
			g.leading = args[0].Float64()
		case "Tm":
			m, err := textBlockMatrix(args, "Tm")
			if err != nil {
				return err
			}
			g.textMatrix, g.lineMatrix = m, m
		case "Td", "TD":
			if len(args) != 2 {
				return fmt.Errorf("%s: want 2 operands, got %d", op, len(args))
			}
			if op == "TD" {
				g.leading = -args[1].Float64()
			}
			g.moveLine(args[0].Float64(), args[1].Float64())
		case "T*":
			g.moveLine(0, -g.leading)
		case "Tj":
			if len(args) != 1 {
				return fmt.Errorf("Tj: want 1 operand, got %d", len(args))
			}
			return g.showStrings([]Value{args[0]}, blocks.add)
		case "TJ":
			if len(args) != 1 || args[0].Kind() != Array {
				return errors.New("TJ: want one array operand")
			}
			parts := make([]Value, args[0].Len())
			for i := range parts {
				parts[i] = args[0].Index(i)
			}
			return g.showStrings(parts, blocks.add)
		case "'":
			if len(args) != 1 {
				return fmt.Errorf("': want 1 operand, got %d", len(args))
			}
			g.moveLine(0, -g.leading)
			return g.showStrings([]Value{args[0]}, blocks.add)
		case "\"":
			if len(args) != 3 {
				return fmt.Errorf("\": want 3 operands, got %d", len(args))
			}
			g.wordSpacing = args[0].Float64()
			g.charSpacing = args[1].Float64()
			g.moveLine(0, -g.leading)
			return g.showStrings([]Value{args[2]}, blocks.add)
		case "Do":
			if len(args) != 1 || args[0].Kind() != Name {
				return fmt.Errorf("Do: want one name operand")
			}
			if depth >= maxFormXObjectDepth {
				return fmt.Errorf("Form XObject nesting exceeds maximum depth %d", maxFormXObjectDepth)
			}
			form := resources.Key("XObject").Key(args[0].Name())
			if form.Kind() != Stream || form.Key("Subtype").Name() != "Form" {
				return nil
			}
			if form.ptr.id != 0 {
				if activeForms[form.ptr] {
					return fmt.Errorf("cyclic Form XObject reference %d %d", form.ptr.id, form.ptr.gen)
				}
				activeForms[form.ptr] = true
				defer delete(activeForms, form.ptr)
			}

			outer := *g
			defer func() { *g = outer }()
			if formMatrix, ok := matrixFromValue(form.Key("Matrix")); ok {
				g.ctm = formMatrix.mul(g.ctm)
			}
			formResources := form.Key("Resources")
			if formResources.Kind() == Null {
				formResources = resources
			}
			return walkTextBlockContent(form, formResources, g, blocks, depth+1, activeForms)
		}
		return nil
	})
}

func matrixFromValue(value Value) (matrix, bool) {
	if value.Kind() != Array || value.Len() != 6 {
		return matrix{}, false
	}
	m := ident
	for i := 0; i < 6; i++ {
		m[i/2][i%2] = value.Index(i).Float64()
	}
	return m, true
}

type textBlockCoalescer struct {
	executor func(TextBlock) error
	pending  *TextBlock
}

func (c *textBlockCoalescer) add(next TextBlock) error {
	if c.pending == nil {
		c.pending = &next
		return nil
	}
	if canMergeTextBlocks(*c.pending, next) {
		c.pending.Text += next.Text
		minX := math.Min(c.pending.X, next.X)
		minY := math.Min(c.pending.Y, next.Y)
		maxX := math.Max(c.pending.X+c.pending.Width, next.X+next.Width)
		maxY := math.Max(c.pending.Y+c.pending.Height, next.Y+next.Height)
		c.pending.X, c.pending.Y = minX, minY
		c.pending.Width, c.pending.Height = maxX-minX, maxY-minY
		c.pending.trailingSpace = next.trailingSpace
		return nil
	}
	if err := c.flush(); err != nil {
		return err
	}
	c.pending = &next
	return nil
}

func (c *textBlockCoalescer) flush() error {
	if c.pending == nil {
		return nil
	}
	block := *c.pending
	c.pending = nil
	return c.executor(block)
}

func canMergeTextBlocks(left, right TextBlock) bool {
	if left.Font != right.Font || math.Abs(left.FontSize-right.FontSize) > 0.01 {
		return false
	}
	if left.trailingSpace || right.leadingSpace {
		return false
	}
	tolerance := math.Max(left.FontSize, 1) * 0.15
	if math.Abs(left.BaselineY-right.BaselineY) > tolerance {
		return false
	}
	gap := right.X - (left.X + left.Width)
	return gap >= -tolerance && gap <= tolerance
}

func startsWithWhitespace(text string) bool {
	for _, r := range text {
		return unicode.IsSpace(r)
	}
	return false
}

func endsWithWhitespace(text string) bool {
	for i := len(text); i > 0; {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		return unicode.IsSpace(r) || size == 0
	}
	return false
}

type textBlockState struct {
	charSpacing     float64
	wordSpacing     float64
	horizontalScale float64
	leading         float64
	rise            float64
	font            Font
	fontSize        float64
	encoding        TextEncoding
	widths          map[string]float64
	textMatrix      matrix
	lineMatrix      matrix
	ctm             matrix
}

type textBlockFont struct {
	font     Font
	encoding TextEncoding
	widths   map[string]float64
}

func (g *textBlockState) moveLine(tx, ty float64) {
	m := matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
	g.lineMatrix = m.mul(g.lineMatrix)
	g.textMatrix = g.lineMatrix
}

func (g *textBlockState) showStrings(parts []Value, executor func(TextBlock) error) error {
	start := g.textMatrix
	var text strings.Builder
	advance := 0.0
	previousWasWhitespace := false
	flush := func() error {
		if text.Len() == 0 {
			return nil
		}
		value := text.String()
		if strings.TrimSpace(value) != "" {
			block := makeTextBlock(value, g.font, g.fontSize, g.rise, advance, start, g.ctm)
			block.leadingSpace = startsWithWhitespace(value)
			block.trailingSpace = endsWithWhitespace(value)
			block.Text = strings.TrimSpace(value)
			if err := executor(block); err != nil {
				return err
			}
		}
		text.Reset()
		advance = 0
		return nil
	}

	for _, part := range parts {
		if part.Kind() != String {
			adjustment := -part.Float64() / 1000 * g.fontSize * g.horizontalScale
			wordGap := math.Max(math.Abs(g.fontSize*g.horizontalScale)*0.15, 0.75)
			if text.Len() > 0 && adjustment > wordGap {
				if err := flush(); err != nil {
					return err
				}
				start = g.textMatrix
			}
			advance += adjustment
			g.advance(adjustment)
			if text.Len() == 0 {
				start = g.textMatrix
				advance = 0
			}
			continue
		}

		raw := part.RawString()
		for _, code := range splitTextCodes(raw, g.encoding) {
			decoded := code
			if g.encoding != nil {
				decoded = g.encoding.Decode(code)
			}
			isWhitespace := strings.TrimSpace(decoded) == ""
			if text.Len() > 0 && previousWasWhitespace && !isWhitespace {
				if err := flush(); err != nil {
					return err
				}
				start = g.textMatrix
			}
			text.WriteString(decoded)
			width, ok := g.widths[code]
			if !ok {
				width = fontCodeWidth(g.font, code)
				g.widths[code] = width
			}
			word := 0.0
			if len(code) == 1 && code[0] == 0x20 {
				word = g.wordSpacing
			}
			delta := (width/1000*g.fontSize + g.charSpacing + word) * g.horizontalScale
			advance += delta
			g.advance(delta)
			previousWasWhitespace = isWhitespace
		}
	}
	return flush()
}

func (g *textBlockState) advance(tx float64) {
	g.textMatrix = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(g.textMatrix)
}

func makeTextBlock(text string, font Font, fontSize, rise, advance float64, tm, ctm matrix) TextBlock {
	ascent, descent := fontVerticalMetrics(font)
	transform := tm.mul(ctm)
	x0, y0 := transformTextPoint(transform, 0, descent/1000*fontSize+rise)
	x1, y1 := transformTextPoint(transform, advance, descent/1000*fontSize+rise)
	x2, y2 := transformTextPoint(transform, advance, ascent/1000*fontSize+rise)
	x3, y3 := transformTextPoint(transform, 0, ascent/1000*fontSize+rise)
	baselineX, baselineY := transformTextPoint(transform, 0, rise)
	effectiveFontSize := fontSize * math.Hypot(transform[1][0], transform[1][1])
	minX := math.Min(math.Min(x0, x1), math.Min(x2, x3))
	maxX := math.Max(math.Max(x0, x1), math.Max(x2, x3))
	minY := math.Min(math.Min(y0, y1), math.Min(y2, y3))
	maxY := math.Max(math.Max(y0, y1), math.Max(y2, y3))
	name := font.BaseFont()
	if i := strings.Index(name, "+"); i >= 0 {
		name = name[i+1:]
	}
	return TextBlock{
		Text: text, X: minX, Y: minY, Width: maxX - minX, Height: maxY - minY,
		BaselineX: baselineX, BaselineY: baselineY, Font: name, FontSize: effectiveFontSize,
	}
}

func transformTextPoint(m matrix, x, y float64) (float64, float64) {
	return x*m[0][0] + y*m[1][0] + m[2][0], x*m[0][1] + y*m[1][1] + m[2][1]
}

func fontVerticalMetrics(font Font) (ascent, descent float64) {
	v := font.V
	if v.Key("Subtype").Name() == "Type0" {
		v = v.Key("DescendantFonts").Index(0)
	}
	descriptor := v.Key("FontDescriptor")
	if descriptor.Key("Ascent").Kind() != Null || descriptor.Key("Descent").Kind() != Null {
		return descriptor.Key("Ascent").Float64(), descriptor.Key("Descent").Float64()
	}
	bbox := descriptor.Key("FontBBox")
	if bbox.Kind() == Array && bbox.Len() == 4 {
		return bbox.Index(3).Float64(), bbox.Index(1).Float64()
	}
	return 0, 0
}

func fontCodeWidth(font Font, code string) float64 {
	if font.V.Key("Subtype").Name() != "Type0" {
		if len(code) == 0 {
			return 0
		}
		width := font.Width(int(code[0]))
		if width != 0 {
			return width
		}
		return font.V.Key("FontDescriptor").Key("MissingWidth").Float64()
	}

	descendant := font.V.Key("DescendantFonts").Index(0)
	cid := 0
	for _, b := range []byte(code) {
		cid = cid<<8 | int(b)
	}
	widths := descendant.Key("W")
	for i := 0; i < widths.Len(); {
		first := int(widths.Index(i).Int64())
		i++
		if i >= widths.Len() {
			break
		}
		next := widths.Index(i)
		if next.Kind() == Array {
			if cid >= first && cid < first+next.Len() {
				return next.Index(cid - first).Float64()
			}
			i++
			continue
		}
		last := int(next.Int64())
		i++
		if i >= widths.Len() {
			break
		}
		width := widths.Index(i).Float64()
		i++
		if first <= cid && cid <= last {
			return width
		}
	}
	if descendant.Key("DW").Kind() != Null {
		return descendant.Key("DW").Float64()
	}
	return 1000
}

func splitTextCodes(raw string, encoding TextEncoding) []string {
	if cmapEncoding, ok := encoding.(*cmap); ok {
		codes := make([]string, 0, len(raw)/2+1)
		for len(raw) > 0 {
			matched := false
			for n := 1; n <= len(cmapEncoding.space) && n <= len(raw); n++ {
				for _, codeSpace := range cmapEncoding.space[n-1] {
					if codeSpace.low <= raw[:n] && raw[:n] <= codeSpace.high {
						codes = append(codes, raw[:n])
						raw = raw[n:]
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				codes = append(codes, raw[:1])
				raw = raw[1:]
			}
		}
		return codes
	}
	codes := make([]string, len(raw))
	for i := range raw {
		codes[i] = raw[i : i+1]
	}
	return codes
}

func textBlockMatrix(args []Value, operator string) (matrix, error) {
	if len(args) != 6 {
		return matrix{}, fmt.Errorf("%s: want 6 operands, got %d", operator, len(args))
	}
	var m matrix
	for i := range args {
		m[i/2][i%2] = args[i].Float64()
	}
	m[2][2] = 1
	return m, nil
}

func popTextArguments(stk *Stack) []Value {
	args := make([]Value, stk.Len())
	for i := len(args) - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
	return args
}

func interpretTextUntil(strm Value, execute func(*Stack, string) error) error {
	var stk Stack
	var dicts []dict
	var readers []io.Reader
	var closers []io.Closer
	if strm.Kind() == Array {
		readers = make([]io.Reader, 0, strm.Len())
		closers = make([]io.Closer, 0, strm.Len())
		for i := 0; i < strm.Len(); i++ {
			reader := strm.Index(i).Reader()
			readers = append(readers, reader)
			closers = append(closers, reader)
		}
	} else {
		reader := strm.Reader()
		readers = []io.Reader{reader}
		closers = []io.Closer{reader}
	}
	defer func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}()

	parser := newBuffer(io.MultiReader(readers...), 0)
	parser.allowEOF = true
	parser.allowObjptr = false
	parser.allowStream = false

reading:
	for {
		tok := parser.readToken()
		if tok == io.EOF {
			break
		}
		if keywordToken, ok := tok.(keyword); ok {
			switch keywordToken {
			case "null", "[", "]", "<<", ">>":
			case "dict":
				stk.Pop()
				stk.Push(newDict())
				continue
			case "currentdict":
				if len(dicts) == 0 {
					return errors.New("no current dictionary")
				}
				stk.Push(Value{nil, objptr{}, dicts[len(dicts)-1]})
				continue
			case "begin":
				dictionary := stk.Pop()
				if dictionary.Kind() != Dict {
					return errors.New("cannot begin non-dictionary")
				}
				dicts = append(dicts, dictionary.data.(dict))
				continue
			case "end":
				if len(dicts) == 0 {
					return errors.New("mismatched begin/end")
				}
				dicts = dicts[:len(dicts)-1]
				continue
			case "def":
				if len(dicts) == 0 {
					return errors.New("def without open dictionary")
				}
				value := stk.Pop()
				key, ok := stk.Pop().data.(name)
				if ok {
					dicts[len(dicts)-1][key] = value.data
				}
				continue
			case "pop":
				stk.Pop()
				continue
			default:
				for i := len(dicts) - 1; i >= 0; i-- {
					if value, ok := dicts[i][name(keywordToken)]; ok {
						stk.Push(Value{nil, objptr{}, value})
						continue reading
					}
				}
				if err := execute(&stk, string(keywordToken)); err != nil {
					return err
				}
				continue
			}
		}
		parser.unreadToken(tok)
		stk.Push(Value{nil, objptr{}, parser.readObject()})
	}
	return nil
}
