// Copyright 2026 Sergey Zonov.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdf

import "errors"

// ErrStopWalking can be returned by a WalkPages callback to stop traversal.
var ErrStopWalking = errors.New("stop walking pages")

// WalkPagesFunc is called for each page in document order, numbered from 1.
type WalkPagesFunc func(num int, page Page) error

type pageNumberRef struct {
	Value int
}

func (r *pageNumberRef) Next() int {
	r.Value++
	return r.Value
}

// WalkPages visits every page in document order without repeatedly traversing
// the page tree. It stops and returns the first error returned by executor.
func (r *Reader) WalkPages(executor WalkPagesFunc) error {
	page := r.Trailer().Key("Root").Key("Pages")
	return walkPages(page, new(pageNumberRef), executor)
}

func walkPages(page Value, pageNumber *pageNumberRef, executor WalkPagesFunc) error {

	kids := page.Key("Kids")

	for i := 0; i < kids.Len(); i++ {
		kid := kids.Index(i)

		switch kid.Key("Type").Name() {
		case "Pages":
			if err := walkPages(kid, pageNumber, executor); err != nil {
				return err
			}
		case "Page":
			if err := executor(pageNumber.Next(), Page{V: kid}); err != nil {
				return err
			}
		}
	}
	return nil
}
