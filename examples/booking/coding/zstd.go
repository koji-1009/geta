// Package coding is the zstd content coding the service offers beside gzip.
// geta builds in gzip only; any other coding is a geta.Coding from the
// application's own dependencies, here github.com/klauspost/compress/zstd.
package coding

import (
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/koji-1009/geta"
)

// pool keeps encoders between responses: one is Reset onto each response
// and goes back once Close has flushed it.
var pool sync.Pool

type pooled struct{ *zstd.Encoder }

func (e pooled) Close() error {
	err := e.Encoder.Close()
	pool.Put(e.Encoder)
	return err
}

// Zstd is the zstd coding (RFC 8878). Its coded form of a body tagged "v1"
// is tagged "v1-zstd", read back as "v1" in If-Match and If-None-Match.
var Zstd = geta.Coding{Name: "zstd", NewWriter: func(w io.Writer) (io.WriteCloser, error) {
	if e, ok := pool.Get().(*zstd.Encoder); ok {
		e.Reset(w)
		return pooled{e}, nil
	}
	e, err := zstd.NewWriter(w, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	return pooled{e}, nil
}}
