package pdf

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// WritePages writes a new PDF containing the requested pages in the supplied
// order. Page numbers are one-based.
//
// Compressed streams are copied directly from the source ReaderAt: images,
// fonts and page content are not decoded or recompressed. Indirect resources
// shared by selected pages are emitted once. Document-level structures such
// as outlines, page labels and AcroForm are intentionally not copied.
func (r *Reader) WritePages(w io.Writer, pages []int) (err error) {
	if w == nil {
		return errors.New("nil PDF writer")
	}
	if len(pages) == 0 {
		return errors.New("no pages selected")
	}
	if r.key != nil || r.trailer["Encrypt"] != nil {
		return errors.New("writing pages from encrypted PDFs is not supported")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok {
				err = recoveredErr
				return
			}
			err = fmt.Errorf("writing PDF pages: %v", recovered)
		}
	}()

	copier := pageCopier{
		r:       r,
		w:       &countingWriter{w: w},
		objects: make([]object, 0, 32),
		mapped:  make(map[objptr]outputRef),
		pages:   make(map[outputRef]bool),
	}
	copier.addObject(nil) // Catalog, replaced below.
	copier.addObject(nil) // Pages, replaced below.

	kids := make(array, 0, len(pages))
	for _, number := range pages {
		if number < 1 || number > r.NumPage() {
			return fmt.Errorf("page %d out of range 1..%d", number, r.NumPage())
		}
		page := r.Page(number)
		if page.V.IsNull() {
			return fmt.Errorf("page %d not found", number)
		}

		pageDict, ok := page.V.data.(dict)
		if !ok {
			return fmt.Errorf("page %d is not a dictionary", number)
		}
		cloned := cloneDict(pageDict)
		delete(cloned, name("Parent"))
		cloned[name("Type")] = name("Page")
		cloned[name("Parent")] = outputRef(2)
		for _, key := range []name{"Resources", "MediaBox", "CropBox", "Rotate"} {
			if _, exists := cloned[key]; exists {
				continue
			}
			if inherited, found := inheritedPageObject(page.V, key); found {
				cloned[key] = inherited
			}
		}

		newPtr := copier.addObject(cloned)
		copier.pages[newPtr] = true
		kids = append(kids, newPtr)
		if page.V.ptr.id != 0 {
			copier.mapped[page.V.ptr] = newPtr
		}
	}

	copier.objects[0] = dict{
		name("Type"):  name("Catalog"),
		name("Pages"): outputRef(2),
	}
	copier.objects[1] = dict{
		name("Type"):  name("Pages"),
		name("Kids"):  kids,
		name("Count"): int64(len(kids)),
	}
	return copier.write()
}

func inheritedPageObject(v Value, key name) (object, bool) {
	for !v.IsNull() {
		if d, ok := v.data.(dict); ok {
			if value, found := d[key]; found {
				return value, true
			}
		}
		v = v.Key("Parent")
	}
	return nil, false
}

func cloneDict(source dict) dict {
	cloned := make(dict, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

type pageCopier struct {
	r       *Reader
	w       *countingWriter
	objects []object
	mapped  map[objptr]outputRef
	pages   map[outputRef]bool
}

type outputRef uint32

func (c *pageCopier) addObject(value object) outputRef {
	c.objects = append(c.objects, value)
	return outputRef(len(c.objects))
}

func (c *pageCopier) mappedReference(old objptr) (outputRef, bool) {
	if ptr, found := c.mapped[old]; found {
		return ptr, true
	}
	value := c.r.resolve(objptr{}, old)
	if value.IsNull() {
		return 0, false
	}
	if d, ok := value.data.(dict); ok {
		switch d[name("Type")] {
		case name("Catalog"), name("Pages"), name("Page"):
			// Do not accidentally pull the original page tree into the subset.
			return 0, false
		}
	}
	ptr := c.addObject(value.data)
	c.mapped[old] = ptr
	return ptr, true
}

func (c *pageCopier) write() error {
	// Resolve the complete reachable graph before assigning object-stream and
	// xref entries. This walks dictionaries only; stream bytes stay in ReaderAt.
	for index := 0; index < len(c.objects); index++ {
		if err := c.discover(c.objects[index]); err != nil {
			return fmt.Errorf("discover object %d: %w", index+1, err)
		}
	}

	type compressedObject struct {
		id   int
		data []byte
	}
	compressed := make([]compressedObject, 0, len(c.objects))
	compressedIndex := make(map[int]int)
	for index, value := range c.objects {
		id := index + 1
		if id <= 2 || c.pages[outputRef(id)] {
			continue
		}
		if _, isStream := value.(stream); isStream {
			continue
		}
		data, err := c.serialize(value)
		if err != nil {
			return fmt.Errorf("serialize object %d: %w", id, err)
		}
		compressedIndex[id] = len(compressed)
		compressed = append(compressed, compressedObject{id: id, data: data})
	}

	objectStreamID := len(c.objects) + 1
	xrefStreamID := objectStreamID + 1
	xref := make([]xrefOutputEntry, xrefStreamID+1)
	xref[0] = xrefOutputEntry{kind: 0, field2: 0, field3: 65535}

	if _, err := io.WriteString(c.w, "%PDF-1.7\n%\xD0\xD4\xC5\xD8\n"); err != nil {
		return err
	}
	for index := 0; index < len(c.objects); index++ {
		id := index + 1
		if compressedAt, ok := compressedIndex[id]; ok {
			xref[id] = xrefOutputEntry{kind: 2, field2: uint64(objectStreamID), field3: uint32(compressedAt)}
			continue
		}
		xref[id] = xrefOutputEntry{kind: 1, field2: uint64(c.w.n)}
		if _, err := fmt.Fprintf(c.w, "%d 0 obj\n", id); err != nil {
			return err
		}
		if err := c.writeIndirectObject(c.objects[index]); err != nil {
			return fmt.Errorf("write object %d: %w", id, err)
		}
		if _, err := io.WriteString(c.w, "\nendobj\n"); err != nil {
			return err
		}
	}

	var objectStreamHeader bytes.Buffer
	var objectStreamBody bytes.Buffer
	for _, item := range compressed {
		if _, err := fmt.Fprintf(&objectStreamHeader, "%d %d ", item.id, objectStreamBody.Len()); err != nil {
			return err
		}
		objectStreamBody.Write(item.data)
		objectStreamBody.WriteByte('\n')
	}
	objectStreamData := append(objectStreamHeader.Bytes(), objectStreamBody.Bytes()...)
	compressedObjectStream, err := deflate(objectStreamData)
	if err != nil {
		return err
	}
	xref[objectStreamID] = xrefOutputEntry{kind: 1, field2: uint64(c.w.n)}
	if _, err := fmt.Fprintf(c.w, "%d 0 obj\n<< /Type /ObjStm /N %d /First %d /Length %d /Filter /FlateDecode >>\nstream\n", objectStreamID, len(compressed), objectStreamHeader.Len(), len(compressedObjectStream)); err != nil {
		return err
	}
	if _, err := c.w.Write(compressedObjectStream); err != nil {
		return err
	}
	if _, err := io.WriteString(c.w, "\nendstream\nendobj\n"); err != nil {
		return err
	}

	xrefOffset := c.w.n
	xref[xrefStreamID] = xrefOutputEntry{kind: 1, field2: uint64(xrefOffset)}
	xrefData := make([]byte, 0, len(xref)*13)
	for _, entry := range xref {
		xrefData = append(xrefData,
			entry.kind,
			byte(entry.field2>>56), byte(entry.field2>>48), byte(entry.field2>>40), byte(entry.field2>>32),
			byte(entry.field2>>24), byte(entry.field2>>16), byte(entry.field2>>8), byte(entry.field2),
			byte(entry.field3>>24), byte(entry.field3>>16), byte(entry.field3>>8), byte(entry.field3),
		)
	}
	compressedXref, err := deflate(xrefData)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "%d 0 obj\n<< /Type /XRef /Size %d /Root 1 0 R /W [1 8 4] /Index [0 %d] /Length %d /Filter /FlateDecode >>\nstream\n", xrefStreamID, len(xref), len(xref), len(compressedXref)); err != nil {
		return err
	}
	if _, err := c.w.Write(compressedXref); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xrefOffset); err != nil {
		return err
	}
	return nil
}

type xrefOutputEntry struct {
	kind   byte
	field2 uint64
	field3 uint32
}

func deflate(data []byte) ([]byte, error) {
	var output bytes.Buffer
	zw := zlib.NewWriter(&output)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (c *pageCopier) discover(value object) error {
	switch value := value.(type) {
	case nil, bool, int64, float64, string, name, outputRef:
		return nil
	case objptr:
		c.mappedReference(value)
		return nil
	case array:
		for _, item := range value {
			if err := c.discover(item); err != nil {
				return err
			}
		}
		return nil
	case dict:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, string(key))
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := c.discover(value[name(key)]); err != nil {
				return err
			}
		}
		return nil
	case stream:
		keys := make([]string, 0, len(value.hdr))
		for key := range value.hdr {
			if key == name("Length") {
				continue
			}
			keys = append(keys, string(key))
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := c.discover(value.hdr[name(key)]); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported object type %T", value)
	}
}

func (c *pageCopier) serialize(value object) ([]byte, error) {
	var output bytes.Buffer
	previous := c.w
	c.w = &countingWriter{w: &output}
	err := c.writeValue(value)
	c.w = previous
	if err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (c *pageCopier) writeIndirectObject(value object) error {
	if valueStream, ok := value.(stream); ok {
		lengthValue := c.r.resolve(valueStream.ptr, valueStream.hdr[name("Length")])
		length := lengthValue.Int64()
		if length < 0 {
			return errors.New("negative stream length")
		}
		header := cloneDict(valueStream.hdr)
		header[name("Length")] = length
		if err := c.writeValue(header); err != nil {
			return err
		}
		if _, err := io.WriteString(c.w, "\nstream\n"); err != nil {
			return err
		}
		written, err := io.CopyN(c.w, io.NewSectionReader(c.r.f, valueStream.offset, length), length)
		if err != nil {
			return err
		}
		if written != length {
			return io.ErrUnexpectedEOF
		}
		_, err = io.WriteString(c.w, "\nendstream")
		return err
	}
	return c.writeValue(value)
}

func (c *pageCopier) writeValue(value object) error {
	switch value := value.(type) {
	case nil:
		_, err := io.WriteString(c.w, "null")
		return err
	case bool:
		_, err := io.WriteString(c.w, strconv.FormatBool(value))
		return err
	case int64:
		_, err := io.WriteString(c.w, strconv.FormatInt(value, 10))
		return err
	case float64:
		_, err := io.WriteString(c.w, strconv.FormatFloat(value, 'g', -1, 64))
		return err
	case string:
		if _, err := io.WriteString(c.w, "<"); err != nil {
			return err
		}
		const hex = "0123456789ABCDEF"
		encoded := make([]byte, len(value)*2)
		for i := range value {
			encoded[i*2] = hex[value[i]>>4]
			encoded[i*2+1] = hex[value[i]&0x0f]
		}
		if _, err := c.w.Write(encoded); err != nil {
			return err
		}
		_, err := io.WriteString(c.w, ">")
		return err
	case name:
		return c.writeName(value)
	case objptr:
		mapped, ok := c.mappedReference(value)
		if !ok {
			_, err := io.WriteString(c.w, "null")
			return err
		}
		_, err := fmt.Fprintf(c.w, "%d 0 R", mapped)
		return err
	case outputRef:
		_, err := fmt.Fprintf(c.w, "%d 0 R", value)
		return err
	case array:
		if _, err := io.WriteString(c.w, "["); err != nil {
			return err
		}
		for index, item := range value {
			if index > 0 {
				if _, err := io.WriteString(c.w, " "); err != nil {
					return err
				}
			}
			if err := c.writeValue(item); err != nil {
				return err
			}
		}
		_, err := io.WriteString(c.w, "]")
		return err
	case dict:
		if _, err := io.WriteString(c.w, "<<"); err != nil {
			return err
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, string(key))
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := c.writeName(name(key)); err != nil {
				return err
			}
			if _, err := io.WriteString(c.w, " "); err != nil {
				return err
			}
			if err := c.writeValue(value[name(key)]); err != nil {
				return err
			}
		}
		_, err := io.WriteString(c.w, ">>")
		return err
	case stream:
		return errors.New("direct stream object is not supported")
	default:
		return fmt.Errorf("unsupported object type %T", value)
	}
}

func (c *pageCopier) writeName(value name) error {
	if _, err := io.WriteString(c.w, "/"); err != nil {
		return err
	}
	const hex = "0123456789ABCDEF"
	for _, b := range []byte(value) {
		if b >= 33 && b <= 126 && b != '#' && !isDelim(b) {
			if _, err := c.w.Write([]byte{b}); err != nil {
				return err
			}
			continue
		}
		if _, err := c.w.Write([]byte{'#', hex[b>>4], hex[b&0x0f]}); err != nil {
			return err
		}
	}
	return nil
}
