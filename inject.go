package atp

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// maxInlineHTML bounds the response size atp will buffer to inject head
// fragments. Larger HTML is streamed through untouched (with a warning-free
// fallback: no injection rather than a huge buffer).
const maxInlineHTML = 4 << 20

// injectWriter buffers HTML responses so the head manager can inject before
// </head>, applies the response header policy just before the header is sent,
// and optionally gzips the response. Non-HTML responses stream straight
// through.
type injectWriter struct {
	http.ResponseWriter
	s       *Server
	r       *http.Request
	inject  bool
	decided bool
	status  int
	written bool // the underlying WriteHeader() has been called

	buffering bool
	compress  bool
	buf       bytes.Buffer
	gz        *gzip.Writer
}

func newInjectWriter(w http.ResponseWriter, r *http.Request, s *Server, inject bool) *injectWriter {
	return &injectWriter{ResponseWriter: w, s: s, r: r, inject: inject, status: http.StatusOK}
}

func (iw *injectWriter) prepare() {
	if iw.s != nil {
		iw.s.headers.applyResponse(iw.Header(), iw.r)
	}
}

func (iw *injectWriter) decide() {
	if iw.decided {
		return
	}
	iw.decided = true
	ct := iw.Header().Get("Content-Type")
	isHTML := strings.Contains(strings.ToLower(ct), "text/html")
	// Never buffer a partial-content or ranged response: injection would
	// corrupt it.
	if iw.r.Header.Get("Range") != "" || iw.status == http.StatusPartialContent {
		isHTML = false
	}
	if iw.r.Method == http.MethodHead {
		isHTML = false
	}
	iw.buffering = iw.inject && isHTML
	iw.compress = shouldGzip(iw.r, ct)
}

func shouldGzip(r *http.Request, ct string) bool {
	if r == nil {
		return false
	}
	if r.Method == http.MethodHead || r.Header.Get("Range") != "" {
		return false
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip") {
		return false
	}
	return compressible(ct)
}

func (iw *injectWriter) WriteHeader(status int) {
	if iw.decided {
		return
	}
	iw.status = status
	iw.decide()
	if !iw.buffering {
		iw.start()
	}
}

// start sends the header and, when compressing, opens the gzip stream.
func (iw *injectWriter) start() {
	iw.prepare()
	if iw.compress {
		iw.Header().Del("Content-Length")
		iw.Header().Set("Content-Encoding", "gzip")
		iw.Header().Add("Vary", "Accept-Encoding")
	}
	iw.ResponseWriter.WriteHeader(iw.status)
	iw.written = true
	if iw.compress {
		iw.gz = gzip.NewWriter(iw.ResponseWriter)
	}
}

func (iw *injectWriter) Write(p []byte) (int, error) {
	if !iw.decided {
		if iw.Header().Get("Content-Type") == "" && len(p) > 0 {
			iw.Header().Set("Content-Type", http.DetectContentType(p))
		}
		iw.decide()
		if !iw.buffering {
			iw.start()
		}
	}
	if iw.buffering {
		if iw.buf.Len()+len(p) > maxInlineHTML {
			iw.flushBuffer()
		} else {
			return iw.buf.Write(p)
		}
	}
	if iw.gz != nil {
		return iw.gz.Write(p)
	}
	return iw.ResponseWriter.Write(p)
}

// flushBuffer stops buffering: it sends what it has (compressed when enabled)
// and switches to streaming.
func (iw *injectWriter) flushBuffer() {
	if !iw.written {
		iw.start()
	}
	if iw.gz != nil {
		_, _ = iw.gz.Write(iw.buf.Bytes())
	} else {
		_, _ = iw.ResponseWriter.Write(iw.buf.Bytes())
	}
	iw.buf.Reset()
	iw.buffering = false
}

// finish injects the head fragments into a buffered HTML document and closes
// any compression stream.
func (iw *injectWriter) finish() {
	if !iw.decided {
		iw.decide()
		if !iw.buffering {
			iw.start()
		}
	}
	if iw.buffering {
		doc := iw.buf.Bytes()
		if iw.inject {
			doc = iw.s.head.Inject(doc, iw.app(), iw.s.capabilityFragments(iw.app())...)
		}
		iw.prepare()
		iw.Header().Del("Content-Length")
		if iw.compress {
			iw.Header().Set("Content-Encoding", "gzip")
			iw.Header().Add("Vary", "Accept-Encoding")
			var gzb bytes.Buffer
			zw := gzip.NewWriter(&gzb)
			_, _ = zw.Write(doc)
			_ = zw.Close()
			doc = gzb.Bytes()
		}
		iw.Header().Set("Content-Length", strconv.Itoa(len(doc)))
		iw.ResponseWriter.WriteHeader(iw.status)
		iw.written = true
		_, _ = iw.ResponseWriter.Write(doc)
		iw.buffering = false
	}
	if iw.gz != nil {
		_ = iw.gz.Close()
		iw.gz = nil
	}
}

func (iw *injectWriter) app() string {
	if iw.s == nil {
		return ""
	}
	return iw.s.appForPath(iw.r.URL.Path)
}

// Flush implements http.Flusher.
func (iw *injectWriter) Flush() {
	if iw.buffering {
		iw.flushBuffer()
	}
	if f, ok := iw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
