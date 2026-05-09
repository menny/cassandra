package user

import "context"

type User struct {
	ID   string
	Name string
}

// SaveUser persists a user. It now requires a non-nil User pointer.
func SaveUser(ctx context.Context, u *User) error {
	if u == nil {
		return context.DeadlineExceeded // Just for simulation
	}
	return nil
}
