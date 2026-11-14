package live

import "context"

// Drain ends every open stream with a bye event (reason drain) and waits until
// no stream is open (ADR 0033 section 7.9). New stream requests then get 503,
// and Ready fails. Drain returns at once when no stream is open. When ctx ends
// first, it returns an error that wraps ctx.Err(). Drain is safe to call more
// than once. Register it with app.WithDrain(hub.Drain).
func (h *Hub) Drain(ctx context.Context) error {
	h.mu.Lock()
	if h.state == stateNew || h.state == stateStarted {
		h.state = stateDraining
	}
	// Every close of drainCh happens under mu, so it happens once.
	select {
	case <-h.drainCh:
	default:
		close(h.drainCh)
	}
	h.mu.Unlock()

	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			return nil
		default:
		}
		return &Error{Op: "drain streams", Err: ctx.Err(), Try: "allow more time for the drain, or check for a stream handler that does not return"}
	}
}
