package importer

import (
	"errors"
	"fmt"

	"github.com/javi11/nntppool/v2/pkg/nntpcli"
)

// guardedNNTPBodyReader contains panics from the external NNTP pool at the
// importer boundary. nntppool v1.5.5 can return a non-nil pooled wrapper whose
// inner reader is a typed nil when BodyReader races with cancellation. Its
// Read, GetYencHeaders, and Close methods then panic instead of returning an
// error. Import probing is best-effort, so turn those panics into normal errors
// and let the caller retry or fall back without taking down active playback.
type guardedNNTPBodyReader struct {
	reader nntpcli.ArticleBodyReader
}

func guardNNTPBodyReader(reader nntpcli.ArticleBodyReader) nntpcli.ArticleBodyReader {
	return &guardedNNTPBodyReader{reader: reader}
}

func (r *guardedNNTPBodyReader) Read(p []byte) (n int, err error) {
	if r == nil || r.reader == nil {
		return 0, errors.New("NNTP body reader is nil")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			n = 0
			err = fmt.Errorf("NNTP body reader panicked while reading: %v", recovered)
		}
	}()
	return r.reader.Read(p)
}

func (r *guardedNNTPBodyReader) GetYencHeaders() (headers nntpcli.YencHeaders, err error) {
	if r == nil || r.reader == nil {
		return nntpcli.YencHeaders{}, errors.New("NNTP body reader is nil")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			headers = nntpcli.YencHeaders{}
			err = fmt.Errorf("NNTP body reader panicked while reading yEnc headers: %v", recovered)
		}
	}()
	return r.reader.GetYencHeaders()
}

func (r *guardedNNTPBodyReader) Close() (err error) {
	if r == nil || r.reader == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("NNTP body reader panicked while closing: %v", recovered)
		}
	}()
	return r.reader.Close()
}
