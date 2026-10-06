package geta

import (
	"bytes"
	"io"
	"math"
	"mime/multipart"
	"os"
	"reflect"
)

// File is a file in a multipart/form-data request body. It is the type of a
// field tagged form in the struct of an input field tagged body:"multipart".
// A File field is required, a *File is optional, and a []File (at least one)
// or *[]File holds every part of its name.
//
// A file's content is held in memory while the request's files fit in
// [Limits.MaxMultipartMemory], and in a temporary file past that. geta
// removes the temporary file when the handler returns; a File is valid until
// then.
//
// A client builds one with [NewFile]; getaclient sends it as one part.
type File struct {
	filename    string
	contentType string
	size        int64
	data        []byte    // the content, held in memory
	path        string    // or the temporary file that holds it
	content     io.Reader // or, for a file a client sends, what it reads
}

// NewFile returns a file for a client to send in a multipart body as one
// part named by the field. contentType defaults to application/octet-stream.
// r is read once, when the request is built.
func NewFile(filename, contentType string, r io.Reader) File {
	return File{filename: filename, contentType: contentType, size: -1, content: r}
}

// Filename returns the part's filename without any directory, as
// [multipart.Part.FileName] does, or "" if there is none.
func (f File) Filename() string { return f.filename }

// ContentType returns the part's Content-Type as sent, or "" if none was
// sent. geta does not check it against the content.
func (f File) ContentType() string { return f.contentType }

// Size returns the content's length in bytes, or -1 for a file from
// [NewFile].
func (f File) Size() int64 { return f.size }

// Open opens the content. A received file may be opened any number of times
// until the handler returns. A file from [NewFile] can be read only once.
func (f File) Open() (io.ReadCloser, error) {
	switch {
	case f.path != "":
		return os.Open(f.path)
	case f.content != nil:
		return io.NopCloser(f.content), nil
	}
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

var fileType = reflect.TypeFor[File]()

// fileKind is a File's kind, as vet.Field.Kind names it.
const fileKind = "geta.File"

// fileSchema is a file's schema, as OpenAPI 3.1 writes a binary part.
func fileSchema() *schema {
	return &schema{Type: "string", ContentMediaType: "application/octet-stream", binary: true}
}

// readFile reads part p into memory if it fits in *mem, the bytes the
// request's files have left, and otherwise into a temporary file whose name
// is appended to tmp.
func readFile(p *multipart.Part, mem *int64, tmp *[]string) (File, error) {
	f := File{filename: p.FileName(), contentType: p.Header.Get("Content-Type")}
	var buf bytes.Buffer
	room := min(max(*mem, 0), math.MaxInt64-1)
	n, err := io.CopyN(&buf, p, room+1)
	if err != nil && err != io.EOF {
		return f, err
	}
	if n <= room {
		*mem -= n
		f.data, f.size = buf.Bytes(), n
		return f, nil
	}
	tf, err := createTemp("", "geta-upload-*")
	if err != nil {
		return f, &storeError{err}
	}
	*tmp = append(*tmp, tf.Name())
	n, err = io.Copy(storeWriter{tf}, io.MultiReader(&buf, p))
	if cerr := tf.Close(); err == nil && cerr != nil {
		err = &storeError{cerr}
	}
	if err != nil {
		return f, err
	}
	f.path, f.size = tf.Name(), n
	return f, nil
}

// createTemp creates a temporary file for an upload. Tests replace it to
// make writes fail.
var createTemp = os.CreateTemp

// storeError reports a file geta could not store: the server's failure, not
// the request's.
type storeError struct{ err error }

func (e *storeError) Error() string { return "storing an uploaded file: " + e.err.Error() }
func (e *storeError) Unwrap() error { return e.err }

// storeWriter wraps write errors in storeError, so they are told apart from
// errors reading the request.
type storeWriter struct{ f *os.File }

func (w storeWriter) Write(b []byte) (int, error) {
	n, err := w.f.Write(b)
	if err != nil {
		err = &storeError{err}
	}
	return n, err
}

// removeFiles removes the temporary files a request's files were held in.
func removeFiles(paths []string) {
	for _, p := range paths {
		os.Remove(p)
	}
}
