// Package model holds the types and errors shared by the store (db) and
// the accounts service.
package model

import (
	"errors"
	"time"
)

// User is an account, without its password hash.
type User struct {
	ID          string
	Username    string
	DisplayName string
	Email       string
	CreatedAt   time.Time
}

// Name is what to call the user: their display name, else username.
func (u User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

var (
	ErrUnknownUser   = errors.New("unknown user")
	ErrUsernameTaken = errors.New("username taken")
	ErrNoSession     = errors.New("no such session")
)
