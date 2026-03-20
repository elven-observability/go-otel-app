package application

import "errors"

func errorAs(err error, target any) bool {
	return errors.As(err, target)
}
