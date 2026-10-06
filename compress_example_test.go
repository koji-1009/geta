package geta_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/koji-1009/geta"
)

// zstd comes from the application's own dependencies; geta builds in gzip
// alone. With github.com/klauspost/compress/zstd, the coding reads:
//
//	// An encoder per response, from a pool: Reset in NewWriter, back to
//	// the pool once Close has flushed it.
//	var zstdPool sync.Pool
//
//	type pooledZstd struct{ *zstd.Encoder }
//
//	func (e pooledZstd) Close() error {
//		err := e.Encoder.Close()
//		zstdPool.Put(e.Encoder)
//		return err
//	}
//
//	zstdCoding := geta.Coding{Name: "zstd", NewWriter: func(w io.Writer) (io.WriteCloser, error) {
//		if e, ok := zstdPool.Get().(*zstd.Encoder); ok {
//			e.Reset(w)
//			return pooledZstd{e}, nil
//		}
//		e, err := zstd.NewWriter(w, zstd.WithEncoderConcurrency(1))
//		if err != nil {
//			return nil, err
//		}
//		return pooledZstd{e}, nil
//	}}
//
//	root := geta.Scope{geta.Compress(zstdCoding, geta.GzipCoding()), geta.ETag()}
//
// Here a stand-in coding, "copy", plays its part.
func ExampleCompress() {
	copying := geta.Coding{Name: "copy", NewWriter: func(w io.Writer) (io.WriteCloser, error) {
		return nopCloser{w}, nil // a real encoder codes what is written; this one copies it
	}}
	app, err := geta.New(geta.Table{
		Root:   geta.Scope{geta.Compress(copying, geta.GzipCoding()), geta.ETag()},
		Routes: []geta.Entry{{Path: "/r", Route: get(textHandler(big))}},
	})
	if err != nil {
		panic(err)
	}
	for _, ae := range []string{"gzip, copy", "gzip;q=0.9, copy;q=0.5", "identity"} {
		req := httptest.NewRequest(http.MethodGet, "/r", nil)
		req.Header.Set("Accept-Encoding", ae)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		fmt.Printf("%s: %q\n", ae, rec.Header().Get("Content-Encoding"))
	}
	// Output:
	// gzip, copy: "copy"
	// gzip;q=0.9, copy;q=0.5: "gzip"
	// identity: ""
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
