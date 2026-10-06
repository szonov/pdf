// Copyright 2026 Sergey Zonov.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import (
	"errors"
	"fmt"
)

// ErrStopTexts stops WalkTexts without interpreting the rest of the page.
var ErrStopTexts = errors.New("stop walking texts")

const maxTextFormXObjectDepth = 12

// WalkTexts interprets text-showing operators on p and calls executor for each
// decoded string in content-stream order. Unlike WalkTextBlocks, it does not
// calculate coordinates, bounds, font metrics, or merge adjacent strings.
// Returning an error stops interpretation; ErrStopTexts can be used for
// expected early termination.
func (p Page) WalkTexts(executor func(string) error) (err error) {
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return nil
	}
	if executor == nil {
		return errors.New("nil text executor")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("interpret texts: %v", recovered)
		}
	}()

	state := textWalkState{encoding: &nopEncoder{}}
	return walkTextContent(
		p.V.Key("Contents"),
		p.Resources(),
		&state,
		executor,
		0,
		make(map[objptr]bool),
	)
}

type textWalkState struct {
	encoding TextEncoding
}

func walkTextContent(
	contents Value,
	resources Value,
	state *textWalkState,
	executor func(string) error,
	depth int,
	activeForms map[objptr]bool,
) error {
	fonts := make(map[string]TextEncoding)
	var graphicsStack []TextEncoding

	emit := func(value Value) error {
		return executor(decodeText(state.encoding, value.RawString()))
	}

	return interpretTextUntil(contents, func(stk *Stack, op string) error {
		args := popTextArguments(stk)

		switch op {
		case "q":
			graphicsStack = append(graphicsStack, state.encoding)
		case "Q":
			if len(graphicsStack) == 0 {
				return errors.New("unmatched Q operator")
			}
			state.encoding = graphicsStack[len(graphicsStack)-1]
			graphicsStack = graphicsStack[:len(graphicsStack)-1]
		case "Tf":
			if len(args) != 2 {
				return fmt.Errorf("Tf: want 2 operands, got %d", len(args))
			}
			fontName := args[0].Name()
			encoding, ok := fonts[fontName]
			if !ok {
				font := Font{V: resources.Key("Font").Key(fontName)}
				encoding = font.Encoder()
				fonts[fontName] = encoding
			}
			state.encoding = encoding
		case "Tj", "'":
			if len(args) != 1 {
				return fmt.Errorf("%s: want 1 operand, got %d", op, len(args))
			}
			return emit(args[0])
		case "\"":
			if len(args) != 3 {
				return fmt.Errorf("\": want 3 operands, got %d", len(args))
			}
			return emit(args[2])
		case "TJ":
			if len(args) != 1 || args[0].Kind() != Array {
				return errors.New("TJ: want one array operand")
			}
			array := args[0]
			for i := 0; i < array.Len(); i++ {
				value := array.Index(i)
				if value.Kind() == String {
					if err := emit(value); err != nil {
						return err
					}
				}
			}
		case "Do":
			if len(args) != 1 || args[0].Kind() != Name {
				return errors.New("Do: want one name operand")
			}
			if depth >= maxTextFormXObjectDepth {
				return fmt.Errorf("Form XObject nesting exceeds maximum depth %d", maxTextFormXObjectDepth)
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

			outer := state.encoding
			defer func() { state.encoding = outer }()
			formResources := form.Key("Resources")
			if formResources.Kind() == Null {
				formResources = resources
			}
			return walkTextContent(form, formResources, state, executor, depth+1, activeForms)
		}
		return nil
	})
}
