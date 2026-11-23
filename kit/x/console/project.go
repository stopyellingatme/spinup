package console

import (
	"errors"
	"io/fs"
	"time"
)

func project(_ fs.FS, _ config, _ time.Time) (overview, error) {
	return overview{}, errors.New("cannot project console facts: the projector is unavailable\nTry: deploy a console image with the projector implementation")
}
