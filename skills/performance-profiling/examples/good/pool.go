package main

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// maxPooledBuffer caps what goes back to the pool. The pool treats every
// buffer as interchangeable, so one 10 MB response would otherwise keep
// 10 MB pinned for every later 1 KB response (golang/go#23199).
const maxPooledBuffer = 64 << 10

// bufPool holds *bytes.Buffer, a pointer, so Put does not allocate to box
// the value into an interface.
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuffer() *bytes.Buffer {
	return bufPool.Get().(*bytes.Buffer)
}

func putBuffer(b *bytes.Buffer) {
	if b.Cap() > maxPooledBuffer {
		return // let the GC take it
	}
	b.Reset()
	bufPool.Put(b)
}

// writeReport builds the response in a pooled buffer and writes it once.
func writeReport(w io.Writer, rows int) error {
	b := getBuffer()
	defer putBuffer(b)
	for i := range rows {
		b.WriteString("row ")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	_, err := w.Write(b.Bytes())
	return err
}

func handleReport(w http.ResponseWriter, r *http.Request) {
	rows, err := strconv.Atoi(r.URL.Query().Get("rows"))
	if err != nil || rows < 0 || rows > 10_000 {
		http.Error(w, "rows must be 0..10000", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writeReport(w, rows) // a failed write means the client went away
}
