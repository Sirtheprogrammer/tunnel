package agent

import (
	"bytes"
	"sync"
	"testing"
)

func TestCaptureRaceSafe(t *testing.T) {
	content := bytes.Repeat([]byte("a"), 1000)
	cap := newCapture(bytes.NewReader(content))

	var wg sync.WaitGroup
	// Reader goroutine
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 100)
			for {
				_, err := cap.Read(buf)
				if err != nil {
					break
				}
			}
		}()
	}

	// Observer goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				cap.result()
			}
		}()
	}

	wg.Wait()
}
