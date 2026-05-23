package main

import (
	"bufio"
	"log"
	"os"
	"sync"
	"time"
)

type TransitionWriter struct {
	fh                 *os.File
	writer             *bufio.Writer
	lock               sync.Mutex
	writer_initialized bool
	quit               chan struct{}
	closeOnce          sync.Once
}

func (w *TransitionWriter) Init(filename string, _ *bool) {
	var err error
	w.fh, err = os.Create(filename)
	if err != nil {
		log.Fatal(err)
	}
	w.writer = bufio.NewWriter(w.fh)
	w.quit = make(chan struct{})
	go func(w *TransitionWriter) {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w.lock.Lock()
				w.writer.Flush()
				w.lock.Unlock()
			case <-w.quit:
				return
			}
		}
	}(w)
	w.writer_initialized = true
}

func (w *TransitionWriter) WriteString(st string) {
	if w.writer_initialized {
		w.lock.Lock()
		w.writer.WriteString(st)
		w.lock.Unlock()
	}
}

func (w *TransitionWriter) Close() {
	if w.writer_initialized {
		w.closeOnce.Do(func() {
			close(w.quit)
			w.lock.Lock()
			defer w.lock.Unlock()
			w.writer.Flush()
			w.fh.Close()
		})
	}
}
