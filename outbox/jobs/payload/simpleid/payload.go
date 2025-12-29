package simpleid

import (
	"errors"
	"fmt"

	"github.com/assurrussa/outbox/shared/types"
)

type identifier interface {
	types.TypeSet
	fmt.Stringer
	IsZero() bool
}

func MustMarshal[T identifier](id T) string {
	v, err := Marshal(id)
	if err != nil {
		panic(err)
	}
	return v
}

func Marshal[T identifier](id T) (string, error) {
	if id.IsZero() {
		return "", errors.New("zero identifier")
	}
	return id.String(), nil
}

func Unmarshal[T identifier](data string) (T, error) {
	return types.Parse[T](data)
}
