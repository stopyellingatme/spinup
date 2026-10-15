package fixture

import (
	"errors"
	"fmt"
	"net/http"
)

// Handler serves requests.
type Handler struct {
	Name   string
	Opts   map[string][]int
	hidden int
	http.Client
}

// Do runs the handler.
func (h *Handler) Do(ctx int, args ...string) (n int, err error) { return 0, nil }

// Value has a value receiver.
func (h Handler) Value() string { return "" }

func (h Handler) unexported() {}

// Store is a contract.
type Store interface {
	Get(key string) ([]byte, error)
	put()
	fmt.Stringer
}

// Mode is a kind.
type Mode int

const (
	// ModeA is first.
	ModeA Mode = iota
	ModeB
	modeC
)

const Limit = 10

var ErrBad = errors.New("bad")

var Default Mode

// Alias is an alias.
type Alias = http.Client

// New builds a Handler.
func New[T any](v T, other ...int) *Handler { return nil }

func hidden() {}

// Old is retired.
//
// Deprecated: since v0.3. Use New instead.
//
//go:fix inline
func Old() *Handler { return New(1) }
