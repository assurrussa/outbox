package validator

import (
	"fmt"

	"github.com/go-playground/validator/v10"

	"github.com/assurrussa/outbox/shared/tools"
)

var Validator = validator.New()

//nolint:gochecknoinits // It`s need
func init() {
	MustRegisterValidation("parse-size", validateParseSize)
}

func MustRegisterValidation(validatorName string, fn validator.Func) {
	err := Validator.RegisterValidation(validatorName, fn)
	if err != nil {
		panic(fmt.Sprintf("validator register %q: %v", validatorName, err))
	}
}

// validateParseSize implements validator.Func.
func validateParseSize(fl validator.FieldLevel) bool {
	count, err := tools.ParseSize(fl.Field().String())
	if count <= 0 {
		return false
	}

	return err == nil
}
