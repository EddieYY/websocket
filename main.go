package websocket

import (
	"errors"
	"io"
	"sync"

	"honnef.co/go/js/dom"

	"github.com/gopherjs/gopherjs/js"
)

type readyState int

const (
	connecting readyState = iota // The connection is not yet open.
	open                         // The connection is open and ready to communicate.
	closing                      // The connection is in the process of closing.
	closed                       // The connection is closed or couldn't be opened.
)

type WebSocket struct {
	js.Object

	pipeReader *PipeReader
	pipeWriter *PipeWriter
}

func New(url string) *WebSocket {
	object := js.Global.Get("WebSocket").New(url)
	ws := &WebSocket{Object: object}
	ws.pipeReader, ws.pipeWriter = Pipe()
	listener := func(messageEvent *dom.MessageEvent) { //gopherjs:blocking
		println("received msg in websocket")
		println(messageEvent.Data.Str())
		go func() { //gopherjs:blocking
			ws.pipeWriter.Write([]byte(messageEvent.Data.Str())) //gopherjs:blocking
		}() //gopherjs:blocking
		println("done receving in websocket")
	} //gopherjs:blocking
	wrapper := func(object js.Object) { listener(&dom.MessageEvent{BasicEvent: &dom.BasicEvent{Object: object}}) }
	ws.Object.Call("addEventListener", "message", wrapper, false)
	//ws.Object.Set("onmessage", wrapper)
	_ = wrapper
	return ws
}

func (ws *WebSocket) OnOpen(listener func(js.Object)) {
	ws.Object.Set("onopen", listener)
}

func (ws *WebSocket) OnClose(listener func(js.Object)) {
	ws.Object.Set("onclose", listener)
}

func (ws *WebSocket) OnMessage(listener func(messageEvent *dom.MessageEvent)) {
	wrapper := func(object js.Object) { listener(&dom.MessageEvent{BasicEvent: &dom.BasicEvent{Object: object}}) }
	ws.Object.Set("onmessage", wrapper)
}

func (ws *WebSocket) Read(p []byte) (n int, err error) { //gopherjs:blocking
	return ws.pipeReader.Read(p) //gopherjs:blocking
} //gopherjs:blocking

func (ws *WebSocket) Write(p []byte) (n int, err error) {
	return len(p), ws.Send(string(p))
}

func (ws *WebSocket) Send(data string) (err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			println(jsErr.Object.Get("name").Str() == "InvalidStateError")
			err = errors.New("InvalidStateError")
		} else {
			panic(e)
		}
	}()
	ws.Object.Call("send", data)
	return nil
}

func (ws *WebSocket) Close() (err error) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if jsErr, ok := e.(*js.Error); ok && jsErr != nil {
			err = jsErr
		} else {
			panic(e)
		}
	}()
	ws.Object.Call("close")
	return nil
}

// ---

// ErrClosedPipe is the error used for read or write operations on a closed pipe.
var ErrClosedPipe = errors.New("io: read/write on closed pipe")

type pipeResult struct {
	n   int
	err error
}

// A pipe is the shared pipe structure underlying PipeReader and PipeWriter.
type pipe struct {
	rl    sync.Mutex    // gates readers one at a time
	wl    sync.Mutex    // gates writers one at a time
	l     sync.Mutex    // protects remaining fields
	data  []byte        // data remaining in pending write
	rwait chan struct{} // waiting reader
	wwait chan struct{} // waiting writer
	rerr  error         // if reader closed, error to give writes
	werr  error         // if writer closed, error to give reads
}

func (p *pipe) read(b []byte) (n int, err error) {
	// One reader at a time.
	p.rl.Lock()
	defer p.rl.Unlock()

	p.l.Lock()
	defer p.l.Unlock()
	for {
		if p.rerr != nil {
			return 0, ErrClosedPipe
		}
		if p.data != nil {
			break
		}
		if p.werr != nil {
			return 0, p.werr
		}
		<-p.rwait
	}
	n = copy(b, p.data)
	p.data = p.data[n:]
	if len(p.data) == 0 {
		p.data = nil
		p.wwait <- struct{}{}
	}
	return
}

var zero [0]byte

func (p *pipe) write(b []byte) (n int, err error) {
	// pipe uses nil to mean not available
	if b == nil {
		b = zero[:]
	}

	// One writer at a time.
	p.wl.Lock()
	defer p.wl.Unlock()

	p.l.Lock()
	defer p.l.Unlock()
	if p.werr != nil {
		err = ErrClosedPipe
		return
	}
	p.data = b
	p.rwait <- struct{}{}
	for {
		if p.data == nil {
			break
		}
		if p.rerr != nil {
			err = p.rerr
			break
		}
		if p.werr != nil {
			err = ErrClosedPipe
		}
		<-p.wwait
	}
	n = len(b) - len(p.data)
	p.data = nil // in case of rerr or werr
	return
}

func (p *pipe) rclose(err error) {
	if err == nil {
		err = ErrClosedPipe
	}
	p.l.Lock()
	defer p.l.Unlock()
	p.rerr = err
	p.rwait <- struct{}{}
	p.wwait <- struct{}{}
}

func (p *pipe) wclose(err error) {
	if err == nil {
		err = io.EOF
	}
	p.l.Lock()
	defer p.l.Unlock()
	p.werr = err
	p.rwait <- struct{}{}
	p.wwait <- struct{}{}
}

// A PipeReader is the read half of a pipe.
type PipeReader struct {
	p *pipe
}

// Read implements the standard Read interface:
// it reads data from the pipe, blocking until a writer
// arrives or the write end is closed.
// If the write end is closed with an error, that error is
// returned as err; otherwise err is EOF.
func (r *PipeReader) Read(data []byte) (n int, err error) { //gopherjs:blocking
	return r.p.read(data) //gopherjs:blocking
} //gopherjs:blocking

// Close closes the reader; subsequent writes to the
// write half of the pipe will return the error ErrClosedPipe.
func (r *PipeReader) Close() error {
	return r.CloseWithError(nil)
}

// CloseWithError closes the reader; subsequent writes
// to the write half of the pipe will return the error err.
func (r *PipeReader) CloseWithError(err error) error {
	r.p.rclose(err)
	return nil
}

// A PipeWriter is the write half of a pipe.
type PipeWriter struct {
	p *pipe
}

// Write implements the standard Write interface:
// it writes data to the pipe, blocking until readers
// have consumed all the data or the read end is closed.
// If the read end is closed with an error, that err is
// returned as err; otherwise err is ErrClosedPipe.
func (w *PipeWriter) Write(data []byte) (n int, err error) {
	return w.p.write(data)
}

// Close closes the writer; subsequent reads from the
// read half of the pipe will return no bytes and EOF.
func (w *PipeWriter) Close() error {
	return w.CloseWithError(nil)
}

// CloseWithError closes the writer; subsequent reads from the
// read half of the pipe will return no bytes and the error err.
func (w *PipeWriter) CloseWithError(err error) error {
	w.p.wclose(err)
	return nil
}

// Pipe creates a synchronous in-memory pipe.
// It can be used to connect code expecting an io.Reader
// with code expecting an io.Writer.
// Reads on one end are matched with writes on the other,
// copying data directly between the two; there is no internal buffering.
// It is safe to call Read and Write in parallel with each other or with
// Close. Close will complete once pending I/O is done. Parallel calls to
// Read, and parallel calls to Write, are also safe:
// the individual calls will be gated sequentially.
func Pipe() (*PipeReader, *PipeWriter) {
	p := new(pipe)
	p.rwait = make(chan struct{})
	p.wwait = make(chan struct{})
	r := &PipeReader{p}
	w := &PipeWriter{p}
	return r, w
}
