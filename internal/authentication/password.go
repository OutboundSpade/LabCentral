package authentication

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltBytes   = 16
	argonKeyBytes    = 32
)

var ErrInvalidCredentials = errors.New("invalid credentials")

func HashPassword(password []byte) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	derived := argon2.IDKey(password, salt, argonIterations, argonMemory, argonParallelism, argonKeyBytes)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derived))
	zero(derived)
	return encoded, nil
}

func VerifyPassword(encoded string, password []byte) error {
	if len(password) > 1024 {
		return ErrInvalidCredentials
	}
	parameters, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return ErrInvalidCredentials
	}
	derived := argon2.IDKey(password, salt, parameters.iterations, parameters.memory, parameters.parallelism, uint32(len(expected)))
	matched := subtle.ConstantTimeCompare(derived, expected) == 1
	zero(derived)
	zero(expected)
	zero(salt)
	if !matched {
		return ErrInvalidCredentials
	}
	return nil
}

type passwordParameters struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

func parsePasswordHash(encoded string) (passwordParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return passwordParameters{}, nil, nil, ErrInvalidCredentials
	}
	var memory, iterations uint32
	var parallelism uint8
	if count, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil || count != 3 ||
		memory != argonMemory || iterations != argonIterations || parallelism != argonParallelism {
		return passwordParameters{}, nil, nil, ErrInvalidCredentials
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != argonSaltBytes {
		return passwordParameters{}, nil, nil, ErrInvalidCredentials
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(expected) != argonKeyBytes {
		zero(salt)
		return passwordParameters{}, nil, nil, ErrInvalidCredentials
	}
	return passwordParameters{memory: memory, iterations: iterations, parallelism: parallelism}, salt, expected, nil
}

func validatePassword(password []byte) error {
	if len(password) < 14 || len(password) > 1024 {
		return fmt.Errorf("password must be between 14 and 1024 bytes")
	}
	return nil
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
